package proxy

import (
	"context"

	"github.com/google/uuid"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

func newTurnRoute(ctx context.Context, env *translate.RequestEnvelope, feats translate.RoutingFeatures, enabledProviders map[string]struct{}, installationID uuid.UUID, strategy router.Strategy, subAgentHint string) routingpolicy.TurnRoute {
	route := routingpolicy.TurnRoute{
		ContextEstimate:             env.ContextOverflowTokenEstimate(),
		TranslationEnabledProviders: enabledProviders,
		InstallationID:              installationID,
		Strategy:                    strategy,
		TurnType:                    turntype.Detect(env, feats, subAgentHint, openCodeCaller(requestcontext.ClientIdentityFrom(ctx))),
		PinTier:                     "miss",
		RequestedTier:               catalog.TierFor(feats.Model),
		StripThinkingBlocks:         stripsThinkingHistory(ctx),
	}
	route.SetCacheShape(turnCacheShape(ctx, env, feats))
	return route
}
