package main

import (
	"context"
	"log/slog"
	"time"

	"workweave/router/internal/flags"
	"workweave/router/internal/observability"
)

const (
	// flagRegistryPublishTimeout bounds a single publish attempt so a slow or
	// unreachable database can't hold a connection for the whole retry budget.
	flagRegistryPublishTimeout = 10 * time.Second
	// flagRegistryPublishRetryInterval and flagRegistryPublishBudget cover the
	// Cloud SQL connector's first-dial latency (routinely >10s on Cloud Run).
	flagRegistryPublishRetryInterval = 5 * time.Second
	flagRegistryPublishBudget        = 5 * time.Minute
)

type flagDefinitionPublisher interface {
	Publish(context.Context, []flags.PublishedDefinition) error
}

// publishFlagRegistry builds PublishedDefinition entries from flags.Registry and
// defaults (injected because they are locals in main), then publishes in the
// background — the table is never on the request path.
func publishFlagRegistry(logger *slog.Logger, publisher flagDefinitionPublisher, defaults map[flags.Key]string) {
	published := make([]flags.PublishedDefinition, 0, len(flags.Registry))
	for _, def := range flags.Registry {
		value, ok := defaults[def.Key]
		if !ok {
			logger.Warn("Flag registered without a published deployment default", "flag", def.Key, "env_var", def.EnvVar)
		}
		published = append(published, flags.PublishedDefinition{Definition: def, DeploymentDefault: value})
	}
	observability.SafeGo(logger, flagRegistryPublishBudget, "flag_registry_publish", func(ctx context.Context) {
		publishFlagRegistryWithRetry(
			ctx, logger, publisher, published,
			flagRegistryPublishTimeout, flagRegistryPublishRetryInterval,
		)
	})
}

// publishFlagRegistryWithRetry retries until ctx expires. Retrying is safe
// because upserts are idempotent and the prune is registry-version guarded.
func publishFlagRegistryWithRetry(
	ctx context.Context,
	logger *slog.Logger,
	publisher flagDefinitionPublisher,
	published []flags.PublishedDefinition,
	attemptTimeout time.Duration,
	retryInterval time.Duration,
) {
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		err := publisher.Publish(attemptCtx, published)
		cancel()
		if err == nil {
			logger.Info("Published flag registry", "count", len(published), "attempts", attempt)
			return
		}

		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			logger.Error("Failed to publish flag registry; per-org flag override UI may be stale", "err", err, "attempts", attempt)
			return
		case <-timer.C:
			logger.Warn("Flag registry publish attempt failed; retrying", "err", err, "attempt", attempt)
		}
	}
}
