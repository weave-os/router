package proxy

import (
	"context"
	"slices"
	"strings"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

// ReasonSessionArmPin marks a turn served by the session-pinned arm: the model
// the router chose on the session's first main-thread turn, kept for the rest
// of the session while session_arm_pin is on. Sibling rescue and the pin-drop
// guards still override it for the affected turn; the arm row is not rewritten.
const ReasonSessionArmPin = "session_arm_pin"

// sessionArmRole is the pin-store role of the arm row. The row is keyed by
// the client session (requestcontext.SessionArmConversationKey), not the
// thread, so a client-side compaction that rewrites the first user message
// does not re-key it and sub-agent threads can find it under mode "all".
const sessionArmRole = "session_arm"

const sessionArmPinTier = "session_arm"

// Reasons stamped on turnLoopResult.SessionArmOverride when an existing arm
// did not serve the turn. The first group is decided by the arm gate itself;
// the second is inferred from the decision that won instead.
const (
	sessionArmOverrideRequestSubset     = "request_allowed_models"
	sessionArmOverrideProvider          = "provider_not_enabled"
	sessionArmOverrideImages            = "not_image_capable"
	sessionArmOverrideSessionDemoted    = "session_demoted"
	sessionArmOverrideAutomaticDisabled = "automatic_routing_disabled"
	sessionArmOverrideOutputLimit       = "output_limit_loop"
	sessionArmOverrideAllowedModels     = "allowed_models"
	sessionArmOverrideExcludedModels    = "excluded_models"
	sessionArmOverrideContextWindow     = "context_window"
	sessionArmOverrideUnsignedHistory   = "unsigned_tool_history"

	sessionArmOverrideForceModel     = "force_model"
	sessionArmOverrideLoopEscalation = "loop_escalation"
	sessionArmOverrideUsageBypass    = "usage_bypass"
	sessionArmOverrideHardPin        = "hard_pin"
	sessionArmOverrideContinuation   = "post_command_continuation"
	sessionArmOverridePolicyPin      = "policy_pin"
	sessionArmOverridePassthrough    = "passthrough"
	sessionArmOverrideOther          = "other"
)

// sessionArmState is the per-turn view of the session-pinned arm.
type sessionArmState struct {
	mode    flags.SessionArmPinMode
	covered bool
	key     [sessionpin.SessionKeyLen]byte
	pin     sessionpin.Pin
	found   bool
}

// sessionArmCoversTurn reports whether mode pins tt. Utility turns are never
// covered: they keep their hard-pin tier.
func sessionArmCoversTurn(mode flags.SessionArmPinMode, tt turntype.TurnType) bool {
	switch tt {
	case turntype.MainLoop, turntype.ToolResult:
		return mode == flags.SessionArmPinMain || mode == flags.SessionArmPinAll
	case turntype.SubAgentDispatch:
		return mode == flags.SessionArmPinAll
	default:
		return false
	}
}

// sessionArmAnchorTurn reports whether tt may write a missing arm. Sub-agent
// turns only inherit; the main thread is what the benchmark assigned.
func sessionArmAnchorTurn(tt turntype.TurnType) bool {
	return tt == turntype.MainLoop || tt == turntype.ToolResult
}

func (s *Service) loadSessionArm(
	ctx context.Context,
	env *translate.RequestEnvelope,
	apiKeyID string,
	threadSessionKey [sessionpin.SessionKeyLen]byte,
	tt turntype.TurnType,
) sessionArmState {
	arm := sessionArmState{mode: s.ResolveSessionArmPin(ctx)}
	if !sessionArmCoversTurn(arm.mode, tt) {
		return arm
	}
	arm.covered = true
	arm.key = deriveConversationSessionKeyForRequest(ctx, env, apiKeyID, threadSessionKey, requestcontext.SessionArmConversationKey)
	arm.pin, arm.found = s.loadPin(ctx, arm.key, sessionArmRole)
	return arm
}

// sessionMaxedModels names the models the previous turn drove to their output
// cap, before the pin-drop guards clear the rows they were read from.
func sessionMaxedModels(pin, hmmHistory sessionpin.Pin) []string {
	var maxed []string
	for _, model := range []string{maxedOutServedModel(pin), maxedOutServedModel(hmmHistory)} {
		if model != "" && !slices.Contains(maxed, model) {
			maxed = append(maxed, model)
		}
	}
	return maxed
}

// sessionArmOverride reports why the arm must not serve this turn, or "" when
// it may. Every check mirrors a pin-drop guard in runTurnLoop so the arm never
// widens the pool: request subset, provider eligibility (including session
// provider strikes), image capability, automatic exclusions (deployment
// disable and session demotion), the previous-turn output-cap loop breaker,
// org allow/exclude lists, and context-window fit re-verified with the
// pre-filter's own estimate. Like the thread pin, an arm the context
// pre-filter excluded but that fits on re-verification is kept: that
// exclusion is a conservative estimate, not a compliance rule.
func (s *Service) sessionArmOverride(
	ctx context.Context,
	env *translate.RequestEnvelope,
	feats translate.RoutingFeatures,
	req router.Request,
	arm sessionpin.Pin,
	demoted, maxed []string,
) string {
	if !modelInRequestSubset(ctx, arm.Model) {
		return sessionArmOverrideRequestSubset
	}
	if req.EnabledProviders != nil {
		if _, ok := req.EnabledProviders[arm.Provider]; !ok {
			return sessionArmOverrideProvider
		}
	}
	if !pinServesImages(arm, req) {
		return sessionArmOverrideImages
	}
	if automaticallyDisabled(req, arm.Model) {
		if slices.Contains(demoted, arm.Model) {
			return sessionArmOverrideSessionDemoted
		}
		return sessionArmOverrideAutomaticDisabled
	}
	if slices.Contains(maxed, baseModelOf(arm.Model)) {
		return sessionArmOverrideOutputLimit
	}
	if _, excluded := req.ExcludedModels[arm.Model]; !excluded {
		return ""
	}
	if _, policyExcluded := s.excludedModelsForRequest(ctx)[arm.Model]; policyExcluded {
		if req.AllowedModels != nil {
			if _, allowed := req.AllowedModels[arm.Model]; !allowed {
				return sessionArmOverrideAllowedModels
			}
		}
		return sessionArmOverrideExcludedModels
	}
	if !sessionArmFitsContext(env, feats, arm) {
		return sessionArmOverrideContextWindow
	}
	if env != nil && env.HasUnsignedToolCallHistory() && gemini3xRequiresSignedHistory(arm.Model) {
		return sessionArmOverrideUnsignedHistory
	}
	return ""
}

// sessionArmFitsContext is the pin path's context re-verification: the
// pre-filter's ÷4 estimate plus the output reserve against the arm's window.
func sessionArmFitsContext(env *translate.RequestEnvelope, feats translate.RoutingFeatures, arm sessionpin.Pin) bool {
	reserve := contextWindowOutputReserve
	if feats.MaxTokens > reserve {
		reserve = feats.MaxTokens
	}
	estimate := env.ContextOverflowTokenEstimate()
	if modelStripsAnthropicSignatures(arm.Model) {
		estimate -= env.SignatureTokenSavings()
	}
	return estimate+reserve <= contextWindowForRequest(arm.Model, arm.Provider)
}

// sessionArmDecision is the arm reconstructed as this turn's decision. It
// carries a rescue order so the dispatch sibling walk can serve an actual
// upstream failure on the arm from a stand-in for this turn; it carries no
// RouteID, so no policy outcome is reported for a held turn.
func (s *Service) sessionArmDecision(arm sessionpin.Pin) router.Decision {
	decision := pinDecision(arm)
	decision.Reason = ReasonSessionArmPin
	decision.Metadata = &router.RoutingMetadata{
		RescueModels:   s.sessionArmRescueOrder(arm),
		PairedProvider: arm.PairedProvider,
		PairedModel:    arm.PairedModel,
	}
	return decision
}

// sessionArmRescueOrder lists the stand-ins a failing arm turn may fall back
// to: the fallback the anchoring policy ranked first, then the routable
// catalog at or below the arm's tier, higher tiers and dearer models first.
// Dispatch still applies the request's exclusions, provider availability and
// context fit to every candidate.
func (s *Service) sessionArmRescueOrder(arm sessionpin.Pin) []string {
	var order []string
	if arm.PairedModel != "" && arm.PairedModel != arm.Model {
		order = append(order, arm.PairedModel)
	}
	armTier := catalog.TierFor(arm.Model)
	var peers []string
	for model := range s.routableUniverse() {
		if model == arm.Model || slices.Contains(order, model) {
			continue
		}
		if tier := catalog.TierFor(model); tier == catalog.TierUnknown || tier > armTier {
			continue
		}
		peers = append(peers, model)
	}
	slices.SortFunc(peers, func(a, b string) int {
		if c := int(catalog.TierFor(b)) - int(catalog.TierFor(a)); c != 0 {
			return c
		}
		pa, _ := catalog.PrimaryPriceFor(a)
		pb, _ := catalog.PrimaryPriceFor(b)
		switch {
		case pb.InputUSDPer1M > pa.InputUSDPer1M:
			return 1
		case pb.InputUSDPer1M < pa.InputUSDPer1M:
			return -1
		}
		return strings.Compare(a, b)
	})
	return append(order, peers...)
}

// sessionArmAnchorDecision is the decision persisted as the arm row: the
// served pick plus, as the row's pair, the first stand-in the anchoring
// policy ranked, so later rescues on the arm start where the policy would.
func sessionArmAnchorDecision(decision router.Decision) router.Decision {
	anchored := decision
	anchored.Reason = ReasonSessionArmPin
	anchored.Metadata = nil
	if decision.Metadata == nil {
		return anchored
	}
	md := &router.RoutingMetadata{
		PairedProvider: decision.Metadata.PairedProvider,
		PairedModel:    decision.Metadata.PairedModel,
	}
	for _, model := range decision.Metadata.RescueModels {
		if model == "" || model == decision.Model {
			continue
		}
		md.PairedModel = model
		md.PairedProvider = decision.Metadata.CandidateProviders[model]
		break
	}
	if md.PairedModel == decision.Model {
		md.PairedModel, md.PairedProvider = "", ""
	}
	anchored.Metadata = md
	return anchored
}

// finishSessionArm runs once the turn's decision is final: it stamps the arm
// telemetry, infers the override reason for the branches that returned before
// the arm gate (force-model, hard pin, usage bypass), and anchors a missing
// arm from the first automatic main-thread decision.
func (s *Service) finishSessionArm(ctx context.Context, installationID uuid.UUID, arm sessionArmState, res *turnLoopResult) {
	if !arm.covered {
		return
	}
	res.SessionArmMode = arm.mode
	res.SessionArmKey = arm.key
	if arm.found {
		res.SessionArmModel = arm.pin.Model
		if !res.SessionArmHeld && res.SessionArmOverride == "" {
			res.SessionArmOverride = sessionArmImplicitOverride(*res)
			if res.SessionArmOverride == "" {
				res.SessionArmOverride = sessionArmOverrideOther
			}
		}
		return
	}
	if !sessionArmAnchorTurn(res.TurnType) || !sessionArmAutomaticDecision(*res) {
		return
	}
	if installationID == uuid.Nil {
		return
	}
	s.writeNewPin(ctx, installationID, arm.key, sessionArmRole, sessionArmAnchorDecision(res.Decision))
	res.SessionArmAnchored = true
	res.SessionArmModel = res.Decision.Model
	observability.FromContext(ctx).Info("session arm anchored",
		"arm_model", res.Decision.Model,
		"arm_provider", res.Decision.Provider,
		"anchor_reason", res.Decision.Reason,
		"mode", string(arm.mode),
		"turn_type", string(res.TurnType),
	)
}

// repinSessionArmOffRefusingModel moves the arm row along with the thread's
// safety-refusal re-pin when the refusing model was the arm itself. Without
// it the next covered turn would hold the session back onto the model that
// just refused; a refusal is a failure of the assigned arm on this task, not
// a policy re-decision. A rescued stand-in that refuses leaves the arm alone.
func (s *Service) repinSessionArmOffRefusingModel(ctx context.Context, routeRes turnLoopResult, served router.Decision, fallback sessionpin.Pin) {
	if routeRes.SessionArmModel == "" || routeRes.SessionArmModel != served.Model {
		return
	}
	if routeRes.SessionArmKey == ([sessionpin.SessionKeyLen]byte{}) {
		return
	}
	arm := fallback
	arm.SessionKey = routeRes.SessionArmKey
	arm.Role = sessionArmRole
	arm.PairedProvider, arm.PairedModel = "", ""
	log := observability.FromContext(ctx)
	if err := s.pinStore.Upsert(context.Background(), arm); err != nil {
		log.Error("safety refusal: session arm upsert failed", "err", err, "from_model", served.Model, "to_model", arm.Model)
		return
	}
	log.Info("safety refusal — session arm moved off refusing model",
		"from_model", served.Model,
		"to_model", arm.Model,
		"to_provider", arm.Provider)
}

// sessionArmAutomaticDecision reports whether the turn's decision was the
// router's own choice. Explicit forces, hard pins, and the subscription
// pass-through are not arms the benchmark assigned.
func sessionArmAutomaticDecision(res turnLoopResult) bool {
	if res.Decision.Model == "" || res.Decision.Provider == "" {
		return false
	}
	return sessionArmImplicitOverride(res) == ""
}

// sessionArmImplicitOverride names the branch that won before the arm gate
// ran, or "" when the decision came from automatic routing.
func sessionArmImplicitOverride(res turnLoopResult) string {
	switch {
	case isUserForcedReason(res.Decision.Reason):
		return sessionArmOverrideForceModel
	case res.Decision.Reason == translate.ReasonLoopEscalation:
		return sessionArmOverrideLoopEscalation
	case res.HardPinned:
		return sessionArmOverrideHardPin
	case res.UsageBypass || res.BlindExperimentPassthrough:
		return sessionArmOverrideUsageBypass
	case res.PinTier == postCommandContinuationPinTier:
		return sessionArmOverrideContinuation
	case res.PinTier == policyPinTier:
		return sessionArmOverridePolicyPin
	case res.Decision.Reason == nativeWebSearchPassthroughReason:
		return sessionArmOverridePassthrough
	default:
		return ""
	}
}

func sessionArmLogFields(res turnLoopResult) []any {
	if res.SessionArmMode == "" || res.SessionArmMode == flags.SessionArmPinOff {
		return nil
	}
	return []any{
		"session_arm_mode", string(res.SessionArmMode),
		"session_arm_model", res.SessionArmModel,
		"session_arm_held", res.SessionArmHeld,
		"session_arm_anchored", res.SessionArmAnchored,
		"session_arm_override", res.SessionArmOverride,
	}
}
