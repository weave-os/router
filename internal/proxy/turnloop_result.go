package proxy

import (
	"context"

	"github.com/google/uuid"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

func newTurnLoopResult(ctx context.Context, env *translate.RequestEnvelope, feats translate.RoutingFeatures, req router.Request, installationID uuid.UUID, subAgentHint string) turnLoopResult {
	return turnLoopResult{
		ContextEstimate:             env.ContextOverflowTokenEstimate(),
		TranslationEnabledProviders: req.EnabledProviders,
		InstallationID:              installationID,
		Strategy:                    router.StrategyFromContext(ctx),
		TurnType:                    turntype.Detect(env, feats, subAgentHint, openCodeCaller(ClientIdentityFrom(ctx))),
		AutoModeReviewer:            turntype.IsSecurityMonitor(env, feats),
		PinTier:                     "miss",
		RequestedTier:               catalog.TierFor(feats.Model),
		StripThinkingBlocks:         betaArtifactHistoryFromContext(ctx),
	}
}
