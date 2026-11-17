package main

import (
	"context"
	"log/slog"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
)

const (
	flagRegistryPublishTimeout       = 10 * time.Second
	flagRegistryPublishRetryInterval = 5 * time.Second
	flagRegistryPublishBudget        = 5 * time.Minute
)

type flagDefinitionPublisher interface {
	Publish(context.Context, []flags.PublishedDefinition) error
}

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
