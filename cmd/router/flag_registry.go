package main

import (
	"context"
	"log/slog"
	"time"

	"workweave/router/internal/flags"
)

const (
	// flagRegistryPublishTimeout bounds a single publish attempt so a slow or
	// unreachable database can't hold a connection for the whole retry budget.
	flagRegistryPublishTimeout = 10 * time.Second
	// flagRegistryPublishRetryInterval and flagRegistryPublishBudget cover a cold
	// start: on Cloud Run the Cloud SQL connector's first dial routinely exceeds
	// ten seconds ("refresh failed: context deadline exceeded"), and the registry
	// publish is the first database write a fresh instance makes, so a single
	// attempt fails on exactly the instances that most need to publish.
	flagRegistryPublishRetryInterval = 5 * time.Second
	flagRegistryPublishBudget        = 5 * time.Minute
)

type flagDefinitionPublisher interface {
	Publish(context.Context, []flags.PublishedDefinition) error
}

// publishFlagRegistry writes internal/flags.Registry to router.flag_definitions,
// pairing each entry with the deployment default resolved above. defaults is
// passed in (not derived) because the resolved values are ordinary locals
// scattered through main(); a missing key is logged and published as empty.
//
// Publishing runs in the background: the table is never read on the request
// path, so boot must not wait on it.
func publishFlagRegistry(logger *slog.Logger, publisher flagDefinitionPublisher, defaults map[flags.Key]string) {
	published := make([]flags.PublishedDefinition, 0, len(flags.Registry))
	for _, def := range flags.Registry {
		value, ok := defaults[def.Key]
		if !ok {
			logger.Warn("Flag registered without a published deployment default", "flag", def.Key, "env_var", def.EnvVar)
		}
		published = append(published, flags.PublishedDefinition{Definition: def, DeploymentDefault: value})
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), flagRegistryPublishBudget)
		defer cancel()
		publishFlagRegistryWithRetry(
			ctx, logger, publisher, published,
			flagRegistryPublishTimeout, flagRegistryPublishRetryInterval,
		)
	}()
}

// publishFlagRegistryWithRetry retries until the publish succeeds or ctx expires.
// Retrying is safe: the upserts are idempotent and the prune is guarded by
// registry version, so a partial write from a timed-out attempt is overwritten
// by the next one.
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
