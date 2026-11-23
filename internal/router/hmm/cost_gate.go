package hmm

import (
	"strings"
	"time"

	"weave-os/router/internal/router/continuity"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

type CostGate continuity.CostGateConfig

type CostGateInput = continuity.CostGateInput

const HistoryReason = "hmm_history"

const DefaultUpgradeConfidenceThreshold = continuity.DefaultUpgradeConfidenceThreshold

const DefaultDowngradeHysteresisTurns = continuity.DefaultDowngradeHysteresisTurns

const (
	ReasonConfidentUpgrade     = "hmm_confident_upgrade"
	ReasonUpgradeConfidenceLow = "hmm_upgrade_confidence_low"
	ReasonPhaseChange          = "hmm_phase_change"
)

func (g CostGate) Decide(input CostGateInput) (sessionpin.Pin, planner.Decision, bool) {
	stayPin, ok := g.StayPin(input.Request, input.ActivePin, input.HMMHistory, input.Now)
	if !ok {
		return stayPin, planner.Decision{Outcome: planner.OutcomeSwitch, Reason: planner.ReasonNoPin}, false
	}
	phaseChanged := IsToolExecutionReason(stayPin.Reason) != IsToolExecutionReason(input.Fresh.Reason)

	cfg := input.Planner
	cfg.TierUpgradeEnabled = false
	stayPrefix, stayPrefixKnown := routingpolicy.CacheablePrefixTokens(stayPin, input.EstimatedInputTokens, input.PrefixBroken)
	plan := planner.Decide(planner.Inputs{
		Pin:                   stayPin,
		Fresh:                 input.Fresh,
		EstimatedInputTokens:  input.EstimatedInputTokens,
		CacheablePrefixTokens: stayPrefix,
		CachePrefixKnown:      stayPrefixKnown,
		PriorOutputTokens:     stayPin.LastOutputTokens,
		AvailableModels:       g.AvailableModels,
		PinCacheCold:          routingpolicy.PinCacheCold(stayPin, input.PrefixBroken, input.Now),
	}, cfg)

	freshIsMoreExpensive := FreshIsMoreExpensive(stayPin.Model, input.Fresh.Model, input.EstimatedInputTokens)
	if phaseChanged && freshIsMoreExpensive {
		plan.Outcome = planner.OutcomeSwitch
		plan.Reason = ReasonPhaseChange
	} else if freshIsMoreExpensive {
		confidence, ok := DecisionConfidence(input.Fresh)
		if ok && confidence >= g.UpgradeConfidenceThreshold {
			plan.Outcome = planner.OutcomeSwitch
			plan.Reason = ReasonConfidentUpgrade
		} else if plan.Outcome != planner.OutcomeSwitch {
			plan.Outcome = planner.OutcomeStay
			plan.Reason = ReasonUpgradeConfidenceLow
		}
	}

	if phaseChanged && plan.Outcome == planner.OutcomeSwitch && plan.Reason == planner.ReasonEVPositive {
		plan.Reason = ReasonPhaseChange
	}

	return stayPin, plan, plan.Outcome == planner.OutcomeStay && stayPin.Model != input.Fresh.Model
}

func (g CostGate) Serve(input CostGateInput, priorServedModel string) continuity.CostGateServe {
	stayPin, plan, sticky := g.Decide(input)
	served := continuity.CostGateServe{Decision: input.Fresh, Planner: plan, Sticky: sticky, StayModel: stayPin.Model, PinTier: costGatePinTier(plan, sticky)}
	if sticky {
		served.Decision = stayPin.Decision()
	}
	if held := EffortHysteresisHold(input.Fresh, priorServedModel, served.Decision.Model, served.Decision.Effort); held != "" {
		served.ChallengerEffort, served.HeldEffort = served.Decision.Effort, held
		served.Decision.Effort = held
		served.Planner.Reason = AppendEffortHysteresisReason(served.Planner.Reason)
	}
	return served
}

func costGatePinTier(plan planner.Decision, sticky bool) string {
	switch {
	case sticky:
		return "hmm_ev_stay_" + plan.Reason
	case plan.Outcome == planner.OutcomeStay && plan.Evaluated():
		return "hmm_ev_same_" + plan.Reason
	case plan.EvaluatedAgainstPin():
		return "hmm_ev_switch_" + plan.Reason
	default:
		return "hmm_fresh_unpinned"
	}
}

func (g CostGate) StayPin(req router.Request, activePin sessionpin.Pin, hmmHistory sessionpin.Pin, now time.Time) (sessionpin.Pin, bool) {
	var (
		best sessionpin.Pin
		ok   bool
	)
	if !IsPinReason(activePin.Reason) {
		activePin = sessionpin.Pin{}
	}
	for _, candidate := range []sessionpin.Pin{activePin, hmmHistory} {
		normalized, candidateOK := g.NormalizeStayPin(req, candidate, now)
		if !candidateOK {
			continue
		}
		if !ok || normalized.LastTurnEndedAt.After(best.LastTurnEndedAt) {
			best = normalized
			ok = true
		}
	}
	return best, ok
}

func (g CostGate) NormalizeStayPin(req router.Request, p sessionpin.Pin, now time.Time) (sessionpin.Pin, bool) {
	served := p.LastServedModel
	if served == "" {
		served = p.Model
	}
	effort, model := routingpolicy.StripEffortSuffix(served)
	if model == "" {
		return sessionpin.Pin{}, false
	}
	if p.LastTurnEndedAt.IsZero() {
		return sessionpin.Pin{}, false
	}
	if !p.PinnedUntil.IsZero() && !p.PinnedUntil.After(now) {
		return sessionpin.Pin{}, false
	}
	if candidates.MaxedOutServedModel(p) != "" {
		return sessionpin.Pin{}, false
	}
	if _, excluded := req.Candidates.ExcludedModels[model]; excluded {
		return sessionpin.Pin{}, false
	}
	if candidates.AutomaticallyDisabled(req, model) {
		return sessionpin.Pin{}, false
	}
	if g.AvailableModels != nil {
		if _, available := g.AvailableModels[model]; !available {
			return sessionpin.Pin{}, false
		}
	}
	if req.HasImages && (!catalog.AcceptsImages(model) ||
		req.ImageCount > 0 && !catalog.AcceptsImageCount(model, req.ImageCount, req.EnabledProviders)) {
		return sessionpin.Pin{}, false
	}
	p.Model = model
	if effort != "" {
		p.Effort = effort
	}
	providerSet := req.EnabledProviders
	if providerSet == nil {
		providerSet = g.RegisteredProviders()
	}
	if p.Provider != "" {
		if _, enabled := providerSet[p.Provider]; enabled {
			pinned := map[string]struct{}{p.Provider: {}}
			if _, valid := catalog.ResolveBindingWithCustom(model, pinned, req.CustomBindings); valid {
				return p, true
			}
		}
	}
	binding, ok := catalog.ResolveBindingWithCustom(model, providerSet, req.CustomBindings)
	if !ok {
		return sessionpin.Pin{}, false
	}
	p.Provider = binding.Provider
	return p, true
}

func IsPinReason(reason string) bool {
	return reason == HistoryReason ||
		strings.HasPrefix(strings.TrimSpace(reason), "hmm_policy")
}

func IsToolExecutionReason(reason string) bool {
	return strings.HasPrefix(strings.TrimSpace(reason), "hmm_policy:tool_execution")
}

func DecisionConfidence(dec router.Decision) (float64, bool) {
	if dec.Metadata == nil {
		return 0, false
	}
	confidence := float64(dec.Metadata.ChosenScore)
	if confidence <= 0 {
		return 0, false
	}
	return confidence, true
}

func FreshIsMoreExpensive(stayModel, freshModel string, inputTokens int) bool {
	stay, okStay := routingpolicy.HMMEffectiveInputUSDPer1M(stayModel, inputTokens)
	fresh, okFresh := routingpolicy.HMMEffectiveInputUSDPer1M(freshModel, inputTokens)
	return okStay && okFresh && fresh > stay
}
