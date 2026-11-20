package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/translate"
)

type turnRequestShape struct {
	StripThinkingBlocks bool
	MessageCount        int
	UserPromptTurn      bool
	CacheWriteTTL       time.Duration
}

func newTurnRequestShape(ctx context.Context, env *translate.RequestEnvelope, feats translate.RoutingFeatures) turnRequestShape {
	return turnRequestShape{
		StripThinkingBlocks: stripsThinkingHistory(ctx),
		MessageCount:        feats.MessageCount,
		UserPromptTurn:      routingEndsWithUserPrompt(ctx, env),
		CacheWriteTTL:       env.CacheWriteTTL(),
	}
}
