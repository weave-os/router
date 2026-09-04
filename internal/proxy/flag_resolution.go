package proxy

import (
	"context"

	"weave-os/router/internal/flags"
)

// Per-organization resolution for the behavioral feature flags registered in
// internal/flags. Precedence is per-org override > deployment default.
// Call sites must use these instead of reading Service fields directly;
// a direct field read silently ignores any per-org override.
// ResolveEmbedOnlyUserMessage lives in service.go (header > per-org > default).

// ResolveStruggleShadowEnabled reports whether the session-level struggle
// detector runs for this request.
func (s *Service) ResolveStruggleShadowEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyStruggleShadowEnabled, s.struggleShadowEnabled)
}

// ResolveStruggleEscalationEnabled reports whether struggling sessions may make
// an early sideways escalation for this request.
func (s *Service) ResolveStruggleEscalationEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyStruggleEscalationEnabled, s.struggleEscalationEnabled)
}

// ResolveStruggleEscalationHoldoutPct returns the percentage of struggle
// detections recorded without escalating, as a self-recovery baseline.
func (s *Service) ResolveStruggleEscalationHoldoutPct(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyStruggleEscalationHoldout, s.struggleEscalationHoldoutPct)
}

// ResolveStruggleEvidenceArming reports whether behavioral spiral evidence may
// arm an escalation for this request, ahead of the turn/wall thresholds.
func (s *Service) ResolveStruggleEvidenceArming(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyStruggleEvidenceArming, s.struggleEvidenceArming)
}

func (s *Service) ResolveSpiralShadowEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeySpiralShadowEnabled, s.spiralShadowEnabled)
}

// ResolveTurnSignalCaptureEnabled reports whether per-turn behavioral
// snapshots may be persisted. Installation privacy gates still take precedence.
func (s *Service) ResolveTurnSignalCaptureEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyTurnSignalCapture, s.turnSignalCaptureEnabled)
}

// ResolveLoopEscalationEnabled reports whether a detected cyclic loop may
// escalate the routed model. Detection telemetry is recorded either way.
func (s *Service) ResolveLoopEscalationEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyLoopEscalationEnabled, s.loopEscalationEnabled)
}

// ResolveLoopEscalationHoldoutPct returns the percentage of loop detections
// recorded without escalating, as a self-recovery baseline.
func (s *Service) ResolveLoopEscalationHoldoutPct(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyLoopEscalationHoldoutPct, s.loopEscalationHoldoutPct)
}

// ResolveTextRepetitionBreakEnabled reports whether the enforcing text-repetition
// loop break is armed for this request.
func (s *Service) ResolveTextRepetitionBreakEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyTextRepetitionBreak, s.textRepetitionBreakEnabled)
}

// ResolvePlannerEnabled reports whether the cache-aware EV planner may propose a
// mid-session switch for this request.
func (s *Service) ResolvePlannerEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyPlannerEnabled, s.plannerEnabled)
}

// ResolveScoreToolResultTurns reports whether tool-result turns are re-scored
// instead of following the session pin.
func (s *Service) ResolveScoreToolResultTurns(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyScoreToolResultTurns, s.scoreToolResultTurns)
}

// ResolvePrefixTrimFreeSwitch reports whether a trimmed prompt prefix counts as a
// free switch point.
func (s *Service) ResolvePrefixTrimFreeSwitch(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyPrefixTrimFreeSwitch, s.prefixTrimFreeSwitch)
}

// ResolveAuthoritativeUpgradeGate reports whether the confidence floor stays
// active for authoritative-per-turn policies.
func (s *Service) ResolveAuthoritativeUpgradeGate(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthoritativeUpgradeGate, s.authoritativeUpgradeGate)
}

// ResolveAuthorityCacheShadow reports whether authoritative-per-turn turns
// record the cache gate's counterfactual verdict. Observation only.
func (s *Service) ResolveAuthorityCacheShadow(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthorityCacheShadow, s.authorityCacheShadow)
}

// ResolveSiblingFailover reports whether an exhausted model may degrade to a
// same-cluster candidate.
func (s *Service) ResolveSiblingFailover(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeySiblingFailover, s.siblingFailover)
}

// ResolveAllowedModelsHeader reports the ROUTER_ALLOWED_MODELS_HEADER flag:
// whether x-weave-allowed-models is honored for an installation that is not
// authorized for policy headers.
func (s *Service) ResolveAllowedModelsHeader(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAllowedModelsHeader, s.allowedModelsHeader)
}

// ResolveOpenAIResponsesBroad reports the ROUTER_OPENAI_RESPONSES_BROAD flag:
// off, only the reasoning+tools turn chat/completions rejects is promoted.
func (s *Service) ResolveOpenAIResponsesBroad(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyOpenAIResponsesBroad, s.openAIResponsesBroad)
}

// ResolveEffortEscalation reports whether policy-requested reasoning-effort
// escalation is applied.
func (s *Service) ResolveEffortEscalation(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyEffortEscalation, s.effortEscalation)
}

// ResolveCyberRefusalRepin reports whether a safety refusal re-pins the
// session off the refusing model.
func (s *Service) ResolveCyberRefusalRepin(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCyberRefusalRepin, s.cyberRefusalRepin)
}

// ResolveCyberRefusalFallbackModel returns the model to re-pin to on a safety
// refusal with no runner-up.
func (s *Service) ResolveCyberRefusalFallbackModel(ctx context.Context) string {
	return flags.StringOr(ctx, flags.KeyCyberRefusalFallback, s.cyberRefusalFallbackModel)
}

// ResolveAnthropicServerSideFallback reports whether Anthropic-targeted
// requests ask Anthropic to re-serve a safety-refused turn on a fallback model.
func (s *Service) ResolveAnthropicServerSideFallback(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAnthropicServerFallback, s.anthropicServerSideFallback)
}
