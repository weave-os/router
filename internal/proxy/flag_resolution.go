package proxy

import (
	"context"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

func (s *Service) ResolvePlannerEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyPlannerEnabled, s.continuityConfig.PlannerEnabled)
}

func (s *Service) ResolveScoreToolResultTurns(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyScoreToolResultTurns, s.continuityConfig.ScoreToolResultTurns)
}

func (s *Service) ResolveHMMToolResultHold(ctx context.Context, strategy router.Strategy) bool {
	return s.strategyPorts.Ownership.Owns(strategy) && flags.BoolOr(ctx, flags.KeyHMMToolResultHold, false)
}

func (s *Service) ResolvePrefixTrimFreeSwitch(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyPrefixTrimFreeSwitch, s.prefixTrimFreeSwitch)
}

func (s *Service) ResolveAuthoritativeUpgradeGate(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthoritativeUpgradeGate, s.hmmConfig.UpgradeGate)
}

func (s *Service) ResolveAuthoritativeUpgradePolicy(ctx context.Context) flags.AuthoritativeUpgradePolicy {
	defaultPolicy := s.hmmConfig.UpgradePolicy
	if defaultPolicy == "" {
		defaultPolicy = flags.AuthoritativeUpgradePolicyScore
	}
	return flags.AuthoritativeUpgradePolicy(flags.StringOr(ctx, flags.KeyAuthoritativeUpgradePolicy, string(defaultPolicy)))
}

func (s *Service) ResolveAuthoritativeUpgradeVotes(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyAuthoritativeUpgradeVotes, s.hmmConfig.UpgradeVotes)
}

func (s *Service) ResolveAuthoritativeDowngradeGate(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthoritativeDowngradeGate, s.hmmConfig.DowngradeGate)
}

func (s *Service) ResolveAuthoritativeUpgradeSlidingThreshold(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthoritativeUpgradeSlidingThreshold, s.hmmConfig.UpgradeSlidingThreshold)
}

func (s *Service) ResolveHMMDowngradeHysteresisTurns(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyHMMDowngradeHysteresisTurns, s.hmmConfig.DowngradeHysteresisTurns)
}

func (s *Service) ResolveHMMDowngradeHysteresisShadowTurns(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyHMMDowngradeHysteresisShadowTurns, s.hmmConfig.DowngradeHysteresisShadowTurns)
}

func (s *Service) ResolveAuthorityCacheShadow(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthorityCacheShadow, s.hmmConfig.AuthorityCacheShadow)
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
