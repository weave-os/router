package proxy

import (
	"context"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

func (s *Service) ResolvePrefixTrimFreeSwitch(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyPrefixTrimFreeSwitch, s.prefixTrimFreeSwitch)
}

func (s *Service) ResolveAllowedModelsHeader(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAllowedModelsHeader, s.ingress.AllowedModelsHeader)
}

func (s *Service) ResolveTransientRateLimit(ctx context.Context) bool {
	return s.throttling.RateLimit.Transient(ctx)
}

func (s *Service) ResolveEffortEscalation(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyEffortEscalation, s.effortEscalation)
}

func plannerConfigFor(ctx context.Context, cfg planner.EVConfig) planner.EVConfig {
	cfg.TierUpgradeEnabled = flags.BoolOr(ctx, flags.KeyPlannerTierUpgrade, cfg.TierUpgradeEnabled)
	cfg.CorrectedEconomics = flags.BoolOr(ctx, flags.KeyPlannerCorrectedEconomics, cfg.CorrectedEconomics)
	return cfg
}

func stripsThinkingHistory(ctx context.Context) bool {
	return conversation.BetaArtifactHistoryFromContext(ctx) || !sessionpin.Enabled(ctx)
}
