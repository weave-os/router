package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

func (s *Service) ResolveTurnSignalCaptureEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyTurnSignalCapture, s.turnSignalCaptureEnabled)
}

func (s *Service) ResolveTextRepetitionBreakEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyTextRepetitionBreak, s.textRepetitionBreakEnabled)
}

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

func (s *Service) ResolveSiblingFailover(ctx context.Context) bool {
	return s.rescueConfig.SiblingFailoverEnabled(ctx)
}

func (s *Service) ResolveAllowedModelsHeader(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAllowedModelsHeader, s.allowedModelsHeader)
}

func (s *Service) ResolveOpenAIResponsesBroad(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyOpenAIResponsesBroad, s.openAIResponsesBroad)
}

func (s *Service) ResolveCCTaskToolsCrossVendor(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCCTaskToolsCrossVendor, s.ccTaskToolsCrossVendor)
}

func (s *Service) ResolveCCAutonomySystemAppend(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCCAutonomySystemAppend, s.ccAutonomySystemAppend)
}

func (s *Service) ResolveEarlyStopRedo(ctx context.Context) flags.EarlyStopRedoMode {
	return s.rescueConfig.EarlyStopRedoMode(ctx)
}

func (s *Service) ResolveCCWorkspaceSystemAppend(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCCWorkspaceSystemAppend, s.ccWorkspaceSystemAppend)
}

func (s *Service) ResolveCommittedStreamArmDemotion(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCommittedStreamArmDemotion, s.committedStreamArmDemotion)
}

func (s *Service) ResolveRescuedFailureArmDemotion(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyRescuedFailureArmDemotion, s.rescuedFailureArmDemotion)
}

func (s *Service) ResolveTransientRateLimit(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyTransientRateLimit, s.transientRateLimit)
}

func (s *Service) ResolveRateLimitCooldown(ctx context.Context) time.Duration {
	seconds := flags.IntOr(ctx, flags.KeyRateLimitCooldownSeconds, s.rateLimitCooldownSeconds)
	if seconds < 1 {
		seconds = DefaultRateLimitCooldownSeconds
	}
	return time.Duration(seconds) * time.Second
}

func (s *Service) ResolveProviderOverloaded(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyProviderOverloaded, s.providerOverloaded)
}

func (s *Service) ResolveNativeAnthropicResponseSignals(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyNativeAnthropicResponseSignals, s.nativeAnthropicResponseSignals)
}

func (s *Service) ResolveNativeOpenAIResponseSignals(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyNativeOpenAIResponseSignals, s.nativeOpenAIResponseSignals)
}

func (s *Service) ResolveEffortEscalation(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyEffortEscalation, s.effortEscalation)
}

func (s *Service) ResolveCyberRefusalRepin(ctx context.Context) bool {
	return s.rescueConfig.CyberRefusalRepinEnabled(ctx)
}

func (s *Service) ResolveCyberRefusalRetry(ctx context.Context) bool {
	return s.rescueConfig.CyberRefusalRetryEnabled(ctx)
}

func (s *Service) ResolveCyberRefusalFallbackModel(ctx context.Context) string {
	return s.rescueConfig.CyberRefusalFallback(ctx)
}

func (s *Service) ResolveAnthropicServerSideFallback(ctx context.Context) bool {
	return s.rescueConfig.AnthropicServerSideFallbackEnabled(ctx)
}

func plannerConfigFor(ctx context.Context, cfg planner.EVConfig) planner.EVConfig {
	cfg.TierUpgradeEnabled = flags.BoolOr(ctx, flags.KeyPlannerTierUpgrade, cfg.TierUpgradeEnabled)
	cfg.CorrectedEconomics = flags.BoolOr(ctx, flags.KeyPlannerCorrectedEconomics, cfg.CorrectedEconomics)
	return cfg
}

func stripsThinkingHistory(ctx context.Context) bool {
	return betaArtifactHistoryFromContext(ctx) || !sessionpin.Enabled(ctx)
}
