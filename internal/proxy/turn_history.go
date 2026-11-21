package proxy

import (
	"context"
	"fmt"
	"log/slog"

	"weave-os/router/internal/translate"
)

func stripClientHistory(ctx context.Context, log *slog.Logger, env *translate.RequestEnvelope, provider string) (context.Context, error) {
	if err := env.StripCommandOutcomes(); err != nil {
		return ctx, fmt.Errorf("strip command outcomes: %w", err)
	}
	if removed, err := env.StripHistoricalCommands(); err != nil {
		return ctx, fmt.Errorf("strip historical router commands: %w", err)
	} else if removed > 0 {
		log.Info("Stripped historical router commands from "+provider+" history", "removed_messages", removed)
	}
	if removed := env.StripRouterFeedbackArtifacts(); removed > 0 {
		log.Info("Stripped router-feedback artifacts from "+provider+" history", "removed_messages", removed)
	}
	if removed := env.StripBetaArtifacts(); removed > 0 {
		ctx = withBetaArtifactHistory(ctx)
		log.Info("Stripped beta artifacts from "+provider+" history", "removed_messages", removed)
	}
	return ctx, nil
}
