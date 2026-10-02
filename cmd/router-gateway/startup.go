package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v5"
)

const (
	startupPostgresTimeout = 120 * time.Second
	startupPingTimeout     = 5 * time.Second
	startupRetryInterval   = time.Second
)

// Direct VPC connectivity can lag container startup. Keep retrying within the
// startup probe window, without opening the listener before Postgres is usable.
func waitForStartupPostgres(ctx context.Context, log *slog.Logger, ping func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, startupPostgresTimeout)
	defer cancel()
	_, err := backoff.Retry(ctx, func() (struct{}, error) {
		if err := ctx.Err(); err != nil {
			return struct{}{}, err
		}
		attemptCtx, attemptCancel := context.WithTimeout(ctx, startupPingTimeout)
		defer attemptCancel()
		return struct{}{}, ping(attemptCtx)
	}, backoff.WithBackOff(backoff.NewConstantBackOff(startupRetryInterval)), backoff.WithMaxElapsedTime(0), backoff.WithNotify(func(err error, delay time.Duration) {
		log.Warn("Gateway Postgres is not ready; retrying startup", "component", "router_gateway", "operation", "boot", "retry_after", delay, "err", err)
	}))
	if err != nil {
		return fmt.Errorf("wait for gateway Postgres startup: %w", err)
	}
	return nil
}

func waitForStartupWorker(ctx context.Context, log *slog.Logger, warm func(context.Context) error) error {
	_, err := backoff.Retry(ctx, func() (struct{}, error) {
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return struct{}{}, warm(attemptCtx)
	}, backoff.WithBackOff(backoff.NewConstantBackOff(startupRetryInterval)), backoff.WithMaxElapsedTime(0), backoff.WithNotify(func(err error, delay time.Duration) {
		log.Warn("Gateway serving binding is not initialized; retrying startup", "retry_after", delay, "err", err)
	}))
	if err != nil {
		return fmt.Errorf("initialize gateway serving binding: %w", err)
	}
	return nil
}

func startupHandler(ready func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ready() {
			http.Error(w, "Gateway initialization is incomplete.", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
