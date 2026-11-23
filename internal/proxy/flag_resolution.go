package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

func (s *Service) ResolvePrefixTrimFreeSwitch(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyPrefixTrimFreeSwitch, s.prefixTrimFreeSwitch)
}

func (s *Service) ResolveSiblingFailover(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeySiblingFailover, s.rescueConfig.SiblingFailover)
}

func (s *Service) ResolveAllowedModelsHeader(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAllowedModelsHeader, s.ingress.AllowedModelsHeader)
}

func (s *Service) ResolveEarlyStopRedo(ctx context.Context) flags.EarlyStopRedoMode {
	return s.rescueConfig.EarlyStopRedoMode(ctx)
}

func (s *Service) ResolveCommittedStreamArmDemotion(ctx context.Context) bool {
	return s.armDemotion.CommittedStreamEnabled(ctx)
}

func (s *Service) ResolveRescuedFailureArmDemotion(ctx context.Context) bool {
	return s.armDemotion.RescuedFailureEnabled(ctx)
}

func (s *Service) ResolveTransientRateLimit(ctx context.Context) bool {
	return s.throttling.RateLimit.Transient(ctx)
}

func (s *Service) ResolveRateLimitCooldown(ctx context.Context) time.Duration {
	return s.throttling.RateLimit.Cooldown(ctx)
}

func (s *Service) ResolveEffortEscalation(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyEffortEscalation, s.effortEscalation)
}

func (s *Service) ResolveCyberRefusalRepin(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCyberRefusalRepin, s.rescueConfig.CyberRefusalRepin)
}

func (s *Service) ResolveCyberRefusalRetry(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCyberRefusalRetry, s.rescueConfig.CyberRefusalRetry)
}

func (s *Service) ResolveCyberRefusalFallbackModel(ctx context.Context) string {
	return flags.StringOr(ctx, flags.KeyCyberRefusalFallback, s.rescueConfig.CyberRefusalFallbackModel)
}

func (s *Service) ResolveAnthropicServerSideFallback(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAnthropicServerFallback, s.rescueConfig.AnthropicServerSideFallback)
}

func plannerConfigFor(ctx context.Context, cfg planner.EVConfig) planner.EVConfig {
	cfg.TierUpgradeEnabled = flags.BoolOr(ctx, flags.KeyPlannerTierUpgrade, cfg.TierUpgradeEnabled)
	cfg.CorrectedEconomics = flags.BoolOr(ctx, flags.KeyPlannerCorrectedEconomics, cfg.CorrectedEconomics)
	return cfg
}

func stripsThinkingHistory(ctx context.Context) bool {
	return conversation.BetaArtifactHistoryFromContext(ctx) || !sessionpin.Enabled(ctx)
}
