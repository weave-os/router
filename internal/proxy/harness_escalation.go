// applyHarnessEscalation clamps harness-bound turns (HarnessMeta,
// SubAgentHarnessMeta, Recovery) to a strong Claude-family model — a weak or
// non-Anthropic upstream that hallucinates harness primitives corrupts client
// harness state. Per-turn only; never touches session pins.
package proxy

import (
	"context"

	"workweave/router/internal/observability"
	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/translate"
)

// Harness-escalation action taxonomy. Exactly one value applies per clamp
// attempt; each records why the clamp engaged or was skipped.
const (
	// harnessActionEscalated: the decision was replaced with escalateModel.
	harnessActionEscalated = "escalated"
	// harnessActionAlreadyStrong: the resolved decision is already a strong
	// Claude-family model — the clamp is a no-op.
	harnessActionAlreadyStrong = "already_strong"
	// harnessActionUsageBypass: the caller's subscription usage-bypass outranks
	// the clamp; the requested model is served straight through.
	harnessActionUsageBypass = "usage_bypass"
	// harnessActionHardPinned: an operator/planner hard pin outranks the clamp;
	// the pinned decision is left in place.
	harnessActionHardPinned = "hard_pinned"
	// harnessActionUserForced: a /force-model pin (or x-weave-force-model)
	// outranks the clamp; the forced model is left in place.
	harnessActionUserForced = "user_forced"
	// harnessActionAlreadyEscalated: the decision was already replaced by the
	// loop-escalation pin (an earlier, stronger escalation) — no further clamp.
	harnessActionAlreadyEscalated = "already_escalated"
	// harnessActionDisabled: the ROUTER_HARNESS_ESCALATION_ENABLED kill switch
	// is off; the clamp does not engage.
	harnessActionDisabled = "disabled"
	// harnessActionProviderIneligible: anthropic is not in the request's
	// enabled-provider set (or the set is empty of anthropic), so the escalate
	// target is unservable; the original decision stands.
	harnessActionProviderIneligible = "provider_ineligible"
	// harnessActionModelExcluded: the request's excluded-models set blocks
	// escalateModel; the original decision stands.
	harnessActionModelExcluded = "model_excluded"
)

// applyHarnessEscalation clamps a resolved harness-protocol turn decision to a
// strong Claude-family model (route up, never down). It rewrites only the
// current decision; pins, PinTier, StickyHit, and Fresh remain unchanged.
func (s *Service) applyHarnessEscalation(ctx context.Context, res *turnLoopResult, req router.Request) {
	if !res.TurnType.HarnessEscalation() {
		return
	}
	log := observability.FromContext(ctx)

	// Precedence: a subscription usage-bypass, an operator/planner hard pin, and
	// a user /force-model pin each outrank the clamp (the same three that
	// outrank loop escalation). A loop-escalation decision already landed on
	// opus — a stronger escalation than this clamp — so it needs no rewrite.
	switch {
	case res.UsageBypass:
		log.Info("router.harness_escalation",
			"action", harnessActionUsageBypass,
			"turn_type", string(res.TurnType),
			"from_model", res.Decision.Model,
			"from_provider", res.Decision.Provider,
			"to_model", escalateModel,
		)
		return
	case res.HardPinned:
		log.Info("router.harness_escalation",
			"action", harnessActionHardPinned,
			"turn_type", string(res.TurnType),
			"from_model", res.Decision.Model,
			"from_provider", res.Decision.Provider,
			"to_model", escalateModel,
		)
		return
	case isUserForcedReason(res.Decision.Reason):
		log.Info("router.harness_escalation",
			"action", harnessActionUserForced,
			"turn_type", string(res.TurnType),
			"from_model", res.Decision.Model,
			"from_provider", res.Decision.Provider,
			"to_model", escalateModel,
		)
		return
	case res.Decision.Reason == translate.ReasonLoopEscalation:
		log.Info("router.harness_escalation",
			"action", harnessActionAlreadyEscalated,
			"turn_type", string(res.TurnType),
			"from_model", res.Decision.Model,
			"from_provider", res.Decision.Provider,
			"to_model", escalateModel,
		)
		return
	case !s.harnessEscalationEnabled:
		log.Info("router.harness_escalation",
			"action", harnessActionDisabled,
			"turn_type", string(res.TurnType),
			"from_model", res.Decision.Model,
			"from_provider", res.Decision.Provider,
			"to_model", escalateModel,
		)
		return
	case catalog.IsClaudeFamily(res.Decision.Model) && catalog.TierFor(res.Decision.Model) >= catalog.TierHigh:
		log.Info("router.harness_escalation",
			"action", harnessActionAlreadyStrong,
			"turn_type", string(res.TurnType),
			"from_model", res.Decision.Model,
			"from_provider", res.Decision.Provider,
			"to_model", escalateModel,
		)
		return
	}

	// Eligible-provider / excluded-model guards: the escalate target must be
	// servable for this request, or clamping would dead-end the turn.
	if req.EnabledProviders != nil {
		if _, ok := req.EnabledProviders[providers.ProviderAnthropic]; !ok {
			log.Info("router.harness_escalation",
				"action", harnessActionProviderIneligible,
				"turn_type", string(res.TurnType),
				"from_model", res.Decision.Model,
				"from_provider", res.Decision.Provider,
				"to_model", escalateModel,
			)
			return
		}
	}
	if _, ok := req.ExcludedModels[escalateModel]; ok {
		log.Info("router.harness_escalation",
			"action", harnessActionModelExcluded,
			"turn_type", string(res.TurnType),
			"from_model", res.Decision.Model,
			"from_provider", res.Decision.Provider,
			"to_model", escalateModel,
		)
		return
	}

	log.Info("router.harness_escalation",
		"action", harnessActionEscalated,
		"turn_type", string(res.TurnType),
		"from_model", res.Decision.Model,
		"from_provider", res.Decision.Provider,
		"to_model", escalateModel,
	)
	res.Decision = router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    escalateModel,
		Reason:   translate.ReasonHarnessEscalation,
	}
	res.HarnessEscalated = true
}
