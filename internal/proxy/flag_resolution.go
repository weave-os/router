package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/flags"
)

func (s *Service) ResolveStruggleShadowEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyStruggleShadowEnabled, s.struggleShadowEnabled)
}

func (s *Service) ResolveSpiralShadowEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeySpiralShadowEnabled, s.spiralShadowEnabled)
}

func (s *Service) ResolveTurnSignalCaptureEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyTurnSignalCapture, s.turnSignalCaptureEnabled)
}

func (s *Service) ResolveLoopEscalationEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyLoopEscalationEnabled, s.loopEscalationEnabled)
}

func (s *Service) ResolveLoopEscalationHoldoutPct(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyLoopEscalationHoldoutPct, s.loopEscalationHoldoutPct)
}

func (s *Service) ResolveTextRepetitionBreakEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyTextRepetitionBreak, s.textRepetitionBreakEnabled)
}

func (s *Service) ResolvePlannerEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyPlannerEnabled, s.plannerEnabled)
}

func (s *Service) ResolveScoreToolResultTurns(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyScoreToolResultTurns, s.scoreToolResultTurns)
}

func (s *Service) ResolveHMMToolResultHold(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyHMMToolResultHold, false)
}

func (s *Service) ResolvePrefixTrimFreeSwitch(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyPrefixTrimFreeSwitch, s.prefixTrimFreeSwitch)
}

func (s *Service) ResolveAuthoritativeUpgradeGate(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthoritativeUpgradeGate, s.authoritativeUpgradeGate)
}

func (s *Service) ResolveAuthoritativeUpgradePolicy(ctx context.Context) flags.AuthoritativeUpgradePolicy {
	defaultPolicy := s.authoritativeUpgradePolicy
	if defaultPolicy == "" {
		defaultPolicy = flags.AuthoritativeUpgradePolicyScore
	}
	return flags.AuthoritativeUpgradePolicy(flags.StringOr(ctx, flags.KeyAuthoritativeUpgradePolicy, string(defaultPolicy)))
}

func (s *Service) ResolveAuthoritativeUpgradeHoldoutPct(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyAuthoritativeUpgradeHoldoutPct, s.authoritativeUpgradeHoldoutPct)
}

func (s *Service) ResolveAuthoritativeUpgradeVotes(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyAuthoritativeUpgradeVotes, s.authoritativeUpgradeVotes)
}

func (s *Service) ResolveAuthoritativeDowngradeGate(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthoritativeDowngradeGate, s.authoritativeDowngradeGate)
}

func (s *Service) ResolveAuthoritativeUpgradeSlidingThreshold(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthoritativeUpgradeSlidingThreshold, s.authoritativeUpgradeSlidingThreshold)
}

func (s *Service) ResolveHMMDowngradeHysteresisTurns(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyHMMDowngradeHysteresisTurns, s.hmmDowngradeHysteresisTurns)
}

func (s *Service) ResolveHMMDowngradeHysteresisShadowTurns(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyHMMDowngradeHysteresisShadowTurns, s.hmmDowngradeHysteresisShadowTurns)
}

func (s *Service) ResolveAuthorityCacheShadow(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthorityCacheShadow, s.authorityCacheShadow)
}

func (s *Service) ResolveSiblingFailover(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeySiblingFailover, s.siblingFailover)
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
	mode, err := flags.ParseEarlyStopRedoMode(flags.StringOr(ctx, flags.KeyEarlyStopRedo, string(s.earlyStopRedo)))
	if err != nil {
		return flags.EarlyStopRedoOff
	}
	return mode
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
	return flags.BoolOr(ctx, flags.KeyCyberRefusalRepin, s.cyberRefusalRepin)
}

func (s *Service) ResolveCyberRefusalRetry(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCyberRefusalRetry, s.cyberRefusalRetry)
}

func (s *Service) ResolveCyberRefusalFallbackModel(ctx context.Context) string {
	return flags.StringOr(ctx, flags.KeyCyberRefusalFallback, s.cyberRefusalFallbackModel)
}

func (s *Service) ResolveAnthropicServerSideFallback(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAnthropicServerFallback, s.anthropicServerSideFallback)
}
