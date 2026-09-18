// Command legacy_runtime_check boots the real worker in both legacy deployment modes.
// Its Postgres fixture must be local; Pub/Sub is an in-process gRPC fixture and no provider is called.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/server"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Legacy runtime compatibility failed", "err", err)
		os.Exit(1)
	}
	slog.Info("Legacy managed and self-hosted runtime compatibility passed")
}

func run() error {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	parsed, err := url.Parse(dsn)
	if err != nil || dsn == "" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		return errors.New("ROUTER_TEST_DATABASE_URL must name an ephemeral loopback Postgres fixture")
	}
	binary, err := filepath.Abs(os.Getenv("ROUTER_TEST_WORKER_BINARY"))
	if err != nil || os.Getenv("ROUTER_TEST_WORKER_BINARY") == "" {
		return errors.New("ROUTER_TEST_WORKER_BINARY must name the real ORT-enabled worker binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	repositories := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	installation, err := repositories.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: uuid.NewString(), Name: "Legacy runtime compatibility"})
	if err != nil {
		return err
	}
	token := auth.GenerateID(auth.APIKeyPrefix)
	hash, prefix, suffix := auth.APITokenFingerprint(token)
	_, err = repositories.APIKeys.Create(ctx, auth.CreateAPIKeyParams{InstallationID: installation.ID, ExternalID: uuid.NewString(), KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix, Scope: auth.ScopeRouting})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	pubsub := grpc.NewServer()
	pubsubpb.RegisterSubscriberServer(pubsub, pubsubFixture{})
	defer pubsub.Stop()
	go func() { _ = pubsub.Serve(listener) }()
	for _, mode := range []server.DeploymentMode{server.DeploymentModeSelfHosted, server.DeploymentModeManaged} {
		if err := checkWorker(ctx, binary, dsn, listener.Addr().String(), token, mode); err != nil {
			return fmt.Errorf("%s worker: %w", mode, err)
		}
	}
	return nil
}

func checkWorker(ctx context.Context, binary, dsn, pubsubAddress, token string, mode server.DeploymentMode) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	logFile, err := os.CreateTemp("", "router-legacy-"+string(mode)+"-*.log")
	if err != nil {
		return err
	}
	defer logFile.Close()
	command := exec.CommandContext(ctx, binary)
	// Do not inherit developer provider keys, cloud credentials, sidecars, or release configuration.
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"DATABASE_URL=" + dsn,
		"PORT=" + fmt.Sprint(port),
		"ROUTER_DEPLOYMENT_MODE=" + string(mode),
		"ROUTER_ONNX_ASSETS_DIR=" + os.Getenv("ROUTER_ONNX_ASSETS_DIR"),
		"ROUTER_ONNX_LIBRARY_DIR=" + os.Getenv("ROUTER_ONNX_LIBRARY_DIR"),
		"PUBSUB_EMULATOR_HOST=" + pubsubAddress,
		"PUBSUB_PROJECT_ID=legacy-runtime-fixture",
		"PUBSUB_TOPIC_ROUTER_INVALIDATION=legacy-invalidation",
		"PUBSUB_SUBSCRIPTION_ROUTER_INVALIDATION=legacy-invalidation",
		"OPENAI_API_KEY=fixture-never-sent-to-provider",
		"OPENAI_BASE_URL=http://127.0.0.1:1",
		"ROUTER_SEMANTIC_CACHE_ENABLED=false",
		"GIN_MODE=release",
		// These must stay unread while the assertion-key opt-in is absent.
		"ROUTER_SERVING_REGISTRY_URI=invalid-disabled-registry",
		"ROUTER_SERVING_CONFIGURATION_URI=invalid-disabled-configuration",
		"ROUTER_SERVING_CONFIGURATION_GENERATION=invalid-disabled-generation",
		"ROUTER_SERVING_TARGET=invalid-disabled-target",
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		return err
	}
	defer func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		_ = command.Wait()
	}()
	client := &http.Client{Timeout: 10 * time.Second}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	readyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		response, err := client.Get(baseURL + "/readyz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("worker did not become ready; inspect %s: %w", logFile.Name(), readyCtx.Err())
		case <-ticker.C:
		}
	}
	for _, probe := range []struct{ path, response string }{
		{path: "/validate", response: `"valid":true`},
		{path: "/v1/models", response: `"models":[]`},
	} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+probe.path, nil)
		if err != nil {
			return err
		}
		request.Header.Set("X-Weave-Router-Key", token)
		request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
		request.Header.Set("X-Weave-Serving-Assertion", "untrusted-ignored-in-legacy-mode")
		if err := expectResponse(client, request, http.StatusOK, probe.response); err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/route", bytes.NewBufferString(`{"model":"claude-sonnet-4-5","max_tokens":32,"messages":[{"role":"user","content":"Explain a binary search."}]}`))
	if err != nil {
		return err
	}
	request.Header.Set("X-Weave-Router-Key", token)
	request.Header.Set("Content-Type", "application/json")
	wantStatus := http.StatusOK
	if mode == server.DeploymentModeManaged {
		// The fixture has no credits: legacy managed billing must still gate inference.
		wantStatus = http.StatusPaymentRequired
	}
	if err := expectResponse(client, request, wantStatus, ""); err != nil {
		return err
	}
	slog.Info("Legacy worker started and served authenticated requests without serving dependencies", "mode", mode, "log", logFile.Name())
	return nil
}

func expectResponse(client *http.Client, request *http.Request, status int, contains string) error {
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8192))
	if err != nil {
		return err
	}
	if response.StatusCode != status || !strings.Contains(string(body), contains) {
		return fmt.Errorf("%s %s returned %d, want %d; body: %s", request.Method, request.URL.Path, response.StatusCode, status, body)
	}
	return nil
}

type pubsubFixture struct {
	pubsubpb.UnimplementedSubscriberServer
}

func (pubsubFixture) CreateSubscription(_ context.Context, subscription *pubsubpb.Subscription) (*pubsubpb.Subscription, error) {
	return subscription, nil
}

func (pubsubFixture) DeleteSubscription(context.Context, *pubsubpb.DeleteSubscriptionRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (pubsubFixture) StreamingPull(stream pubsubpb.Subscriber_StreamingPullServer) error {
	<-stream.Context().Done()
	return stream.Context().Err()
}
