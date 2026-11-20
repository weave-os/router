package proxy

import (
	"context"

	"weave-os/router/internal/translate"
)

type turnRequestShape struct {
	StripThinkingBlocks bool
	MessageCount        int
	UserPromptTurn      bool
}

func newTurnRequestShape(ctx context.Context, env *translate.RequestEnvelope, feats translate.RoutingFeatures) turnRequestShape {
	return turnRequestShape{
		StripThinkingBlocks: stripsThinkingHistory(ctx),
		MessageCount:        feats.MessageCount,
		UserPromptTurn:      routingEndsWithUserPrompt(ctx, env),
	}
}
