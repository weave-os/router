package conversation

import (
	"context"
	"fmt"
	"log/slog"

	"weave-os/router/internal/translate"
)

type betaArtifactHistoryContextKey struct{}

func withBetaArtifactHistory(ctx context.Context) context.Context {
	return context.WithValue(ctx, betaArtifactHistoryContextKey{}, true)
}

func BetaArtifactHistoryFromContext(ctx context.Context) bool {
	found, _ := ctx.Value(betaArtifactHistoryContextKey{}).(bool)
	return found
}

func StripClientHistory(ctx context.Context, log *slog.Logger, env *translate.RequestEnvelope, provider string) (context.Context, error) {
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
	if removed := env.StripRetiredBetaHistory(); removed > 0 {
		ctx = withBetaArtifactHistory(ctx)
		log.Info("Stripped retired beta history from "+provider+" history", "removed_messages", removed)
	}
	if removed := env.StripRetiredBetaNotices(); removed > 0 {
		log.Info("Stripped retired beta notices from "+provider+" history", "removed_messages", removed)
	}
	return ctx, nil
}
