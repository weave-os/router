package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/config"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/gateway"
	"weave-os/router/internal/gateway/iam"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/postgres/pgtls"
	"weave-os/router/internal/postgres/poolconfig"
	"weave-os/router/internal/postgres/serving"
)

func main() {
	if err := run(); err != nil {
		observability.Get().Error("Router gateway stopped", "component", "router_gateway", "operation", "serve", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	environment := policyregistry.Environment(config.MustGet("ROUTER_SERVING_ENVIRONMENT"))
	if err := policyregistry.ValidateEnvironment(environment); err != nil {
		return err
	}
	signer, err := policyregistry.NewAssertionSigner([]byte(strings.TrimSpace(config.MustGet("ROUTER_SERVING_ASSERTION_KEY"))), time.Now)
	if err != nil {
		return err
	}
	poolConfig, err := pgxpool.ParseConfig(config.PostgresDSN())
	if err != nil {
		return err
	}
	poolconfig.ConfigureTimeouts(poolConfig)
	clientTLS, err := pgtls.Configure(poolConfig)
	if err != nil {
		return err
	}
	if clientTLS {
		observability.FromContext(ctx).Info("Postgres connections authenticate with a client certificate", "component", "router_gateway", "operation", "boot")
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = "router,public"
	poolConfig.MaxConns = 6
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnIdleTime = 10 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := waitForStartupPostgres(ctx, observability.FromContext(ctx), pool.Ping); err != nil {
		return err
	}
	registryCtx, registryCancel := context.WithTimeout(ctx, 10*time.Second)
	defer registryCancel()
	registry, err := policyregistry.NewGCSRegistry(registryCtx, config.MustGet("ROUTER_SERVING_REGISTRY_URI"))
	if err != nil {
		return fmt.Errorf("initialize gateway policy registry: %w", err)
	}
	defer registry.Close()
	credentialLookup := serving.CredentialLookup{Queries: dbbudget.Queries(pool)}
	credentials := auth.RoutingCredentialVerifier{Keys: credentialLookup, Subjects: credentialLookup}
	admissions, err := serving.NewServingAdmissionRepo(pool, environment)
	if err != nil {
		return err
	}
	transport := newWorkerTransport()
	defer transport.CloseIdleConnections()
	products := gateway.ProductSurfaces{Environment: environment, InternalToken: strings.TrimSpace(config.GetOr("ROUTER_INTERNAL_SERVICE_TOKEN", "")), Analytics: credentials, Reads: credentials, Feedback: feedback.NewSigner(config.GetOr("ROUTER_FEEDBACK_LINK_SECRET", ""), 0), Attribution: serving.FeedbackLookup{Queries: dbbudget.Queries(pool)}}
	forwarder, err := gateway.NewHandler(credentials, admissions, registry, signer, iam.Authorizer{}, transport, products)
	if err != nil {
		return err
	}
	handler := gatewayHTTPHandler(forwarder, forwarder.ReadinessHandler(pool.Ping), forwarder.StartupHandler(pool.Ping))
	server := &http.Server{Addr: ":" + config.GetOr("PORT", "8080"), Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 620 * time.Second, IdleTimeout: 90 * time.Second}
	stopped := make(chan error, 1)
	go func() { stopped <- server.ListenAndServe() }()
	select {
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 615*time.Second)
		defer drainCancel()
		return server.Shutdown(drainCtx)
	}
}

func newWorkerTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Admission, cold runtime loading and first byte share the gateway request deadline.
	transport.ResponseHeaderTimeout = 0
	return transport
}
