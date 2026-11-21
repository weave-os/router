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
	"net/http/httptest"
	"net/netip"
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
	"weave-os/router/smoke/hmmstub"
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
		return errors.New("ROUTER_TEST_WORKER_BINARY must name the real worker binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	releaseDir, err := os.MkdirTemp("", "router-legacy-policy-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(releaseDir) }()
	if err := hmmstub.WriteRelease(releaseDir); err != nil {
		return err
	}
	classifier := httptest.NewServer(hmmstub.Handler())
	defer classifier.Close()
	policy := policyFixture{releaseDir: releaseDir, classifierURL: classifier.URL}
	pubsub := grpc.NewServer()
	pubsubpb.RegisterSubscriberServer(pubsub, pubsubFixture{})
	defer pubsub.Stop()
	go func() { _ = pubsub.Serve(listener) }()
	if err := checkWorker(ctx, binary, dsn, listener.Addr().String(), token, policy, server.DeploymentModeSelfHosted); err != nil {
		return fmt.Errorf("%s worker: %w", server.DeploymentModeSelfHosted, err)
	}
	return nil
}

func waitForWorkerReady(ctx context.Context, client *http.Client, readyURL string, logFile *os.File) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	readyCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	readyRequest, err := http.NewRequestWithContext(readyCtx, http.MethodGet, readyURL, nil)
	if err != nil {
		return err
	}
	var lastReadiness string
	for {
		response, err := client.Do(readyRequest)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 8192))
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			lastReadiness = fmt.Sprintf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
			if readErr != nil {
				lastReadiness += "; read error: " + readErr.Error()
			}
		} else {
			lastReadiness = err.Error()
		}
		select {
		case <-readyCtx.Done():
			_ = logFile.Sync()
			logTail, _ := os.ReadFile(logFile.Name())
			if len(logTail) > 8192 {
				logTail = logTail[len(logTail)-8192:]
			}
			return fmt.Errorf("worker did not become ready; last /readyz response: %s; worker log tail: %s; timeout: %w", lastReadiness, strings.TrimSpace(string(logTail)), readyCtx.Err())
		case <-ticker.C:
		}
	}
}

type policyFixture struct {
	releaseDir    string
	classifierURL string
}

func checkWorker(ctx context.Context, binary, dsn, pubsubAddress, token string, policy policyFixture, mode server.DeploymentMode) error {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := int(netip.MustParseAddrPort(listener.Addr().String()).Port())
	_ = listener.Close()
	logFile, err := os.CreateTemp("", "router-legacy-"+string(mode)+"-*.log")
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	command := exec.CommandContext(ctx, binary)
	command.Env = workerEnv(dsn, pubsubAddress, port, policy, mode)
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
	if err := waitForWorkerReady(ctx, client, baseURL+"/readyz", logFile); err != nil {
		return err
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
	if err := expectResponse(client, request, http.StatusOK, ""); err != nil {
		return err
	}
	slog.Info("Legacy worker started and served authenticated requests without serving dependencies", "mode", mode, "log", logFile.Name())
	return nil
}

func workerEnv(dsn, pubsubAddress string, port int, policy policyFixture, mode server.DeploymentMode) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"DATABASE_URL=" + dsn,
		"PORT=" + fmt.Sprint(port),
		"ROUTER_DEPLOYMENT_MODE=" + string(mode),
		"PUBSUB_EMULATOR_HOST=" + pubsubAddress,
		"PUBSUB_PROJECT_ID=legacy-runtime-fixture",
		"PUBSUB_TOPIC_ROUTER_INVALIDATION=legacy-invalidation",
		"PUBSUB_SUBSCRIPTION_ROUTER_INVALIDATION=legacy-invalidation",
		"OPENAI_API_KEY=fixture-never-sent-to-provider",
		"OPENAI_BASE_URL=http://127.0.0.1:1",
		"ROUTER_SEMANTIC_CACHE_ENABLED=false",
		"ROUTER_POLICY_RELEASE_DIR=" + policy.releaseDir,
		"ROUTER_HMM_SIDECAR_URL=" + policy.classifierURL,
		"ROUTER_HMM_SIDECAR_AUTH=none",
		"GIN_MODE=release",
	}
	return env
}

func expectResponse(client *http.Client, request *http.Request, status int, contains string) error {
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
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
