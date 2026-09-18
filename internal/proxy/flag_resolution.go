package proxy

import (
	"context"
	"time"

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

// ResolveAuthoritativeDowngradeGate reports whether the confidence floor also
// applies to authoritative-per-turn downgrades.
func (s *Service) ResolveAuthoritativeDowngradeGate(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthoritativeDowngradeGate, s.authoritativeDowngradeGate)
}

// ResolveHMMDowngradeHysteresisTurns returns how many consecutive
// cheaper-than-pin authoritative votes are required before the downgrade is
// applied. 0 disables hysteresis.
func (s *Service) ResolveHMMDowngradeHysteresisTurns(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyHMMDowngradeHysteresisTurns, s.hmmDowngradeHysteresisTurns)
}

// ResolveHMMDowngradeHysteresisShadowTurns returns the hysteresis threshold a
// served authoritative downgrade is shadow-scored against. 0 disables the
// shadow; it never changes routing.
func (s *Service) ResolveHMMDowngradeHysteresisShadowTurns(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyHMMDowngradeHysteresisShadowTurns, s.hmmDowngradeHysteresisShadowTurns)
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

// ResolveCCTaskToolsCrossVendor reports the ROUTER_CC_TASK_TOOLS_CROSSVENDOR
// flag: whether Claude Code's task-list tools and their reminders survive a
// cross-vendor emit. Ignored when the orchestration tools are stripped.
func (s *Service) ResolveCCTaskToolsCrossVendor(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCCTaskToolsCrossVendor, s.ccTaskToolsCrossVendor)
}

// ResolveCCAutonomySystemAppend reports the ROUTER_CC_AUTONOMY_SYSTEM_APPEND
// flag: whether Claude Code main-loop and tool-result turns get
// translate.AutonomySystemText appended to their system prompt.
func (s *Service) ResolveCCAutonomySystemAppend(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCCAutonomySystemAppend, s.ccAutonomySystemAppend)
}

// ResolveCCWorkspaceSystemAppend reports whether Claude Code main-loop and
// tool-result turns served cross-vendor get translate.WorkspaceSystemText
// appended (cc_workspace_system_append; org-overridable).
func (s *Service) ResolveCCWorkspaceSystemAppend(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCCWorkspaceSystemAppend, s.ccWorkspaceSystemAppend)
}

// ResolveCommittedStreamArmDemotion reports the
// ROUTER_COMMITTED_STREAM_ARM_DEMOTION flag: on, a model whose stream failed
// after the prelude committed leaves the session's automatic selection.
func (s *Service) ResolveCommittedStreamArmDemotion(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCommittedStreamArmDemotion, s.committedStreamArmDemotion)
}

// ResolveRescuedFailureArmDemotion reports the
// ROUTER_RESCUED_FAILURE_ARM_DEMOTION flag: on, the primary model of a turn
// whose attempt failed pre-commit and was handed to a same-cluster sibling
// leaves the session's automatic selection.
func (s *Service) ResolveRescuedFailureArmDemotion(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyRescuedFailureArmDemotion, s.rescuedFailureArmDemotion)
}

// ResolveTransientRateLimit reports the ROUTER_TRANSIENT_RATE_LIMIT flag: on,
// a rescued upstream 429 cools the primary arm down for
// ResolveRateLimitCooldown instead of demoting it for the session, the
// in-turn rescue readmits cooling-down arms when honouring them would leave
// no candidate, and same-binding retries of a 429 honour Retry-After.
func (s *Service) ResolveTransientRateLimit(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyTransientRateLimit, s.transientRateLimit)
}

// ResolveRateLimitCooldown is how long a rescued 429 keeps the primary arm out
// of the session's automatic selection (ROUTER_RATE_LIMIT_COOLDOWN_SECONDS).
func (s *Service) ResolveRateLimitCooldown(ctx context.Context) time.Duration {
	seconds := flags.IntOr(ctx, flags.KeyRateLimitCooldownSeconds, s.rateLimitCooldownSeconds)
	if seconds < 1 {
		seconds = DefaultRateLimitCooldownSeconds
	}
	return time.Duration(seconds) * time.Second
}

// ResolveNativeAnthropicResponseSignals reports the
// ROUTER_NATIVE_ANTHROPIC_RESPONSE_SIGNALS flag: whether an Anthropic-native
// passthrough turn records its observed stop_reason and tool_use block count
// on the telemetry row. Observability only.
func (s *Service) ResolveNativeAnthropicResponseSignals(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyNativeAnthropicResponseSignals, s.nativeAnthropicResponseSignals)
}

// ResolveNativeOpenAIResponseSignals reports the
// ROUTER_NATIVE_OPENAI_RESPONSE_SIGNALS flag: whether an OpenAI-native turn
// records its observed finish_reason and tool-call count on the telemetry row.
// Observability only.
func (s *Service) ResolveNativeOpenAIResponseSignals(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyNativeOpenAIResponseSignals, s.nativeOpenAIResponseSignals)
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

// ResolveCyberRefusalRetry reports whether a turn OpenAI declined on cyber
// policy is re-dispatched to the fallback model within the same turn.
func (s *Service) ResolveCyberRefusalRetry(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCyberRefusalRetry, s.cyberRefusalRetry)
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
