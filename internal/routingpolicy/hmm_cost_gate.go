package routingpolicy

import (
	"strings"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

type HMMCostGate struct {
	AvailableModels            map[string]struct{}
	RegisteredProviders        func() map[string]struct{}
	UpgradeConfidenceThreshold float64
}

type HMMCostGateInput struct {
	Request              router.Request
	ActivePin            sessionpin.Pin
	HMMHistory           sessionpin.Pin
	Fresh                router.Decision
	EstimatedInputTokens int
	PrefixBroken         bool
	Planner              planner.EVConfig
	SameTierPin          bool
	Now                  time.Time
}

const HMMHistoryReason = "hmm_history"

const (
	HMMReasonConfidentUpgrade     = "hmm_confident_upgrade"
	HMMReasonUnscoredUpgrade      = "hmm_unscored_upgrade"
	HMMReasonUpgradeConfidenceLow = "hmm_upgrade_confidence_low"
	HMMReasonPhaseChange          = "hmm_phase_change"
)

func (g HMMCostGate) Decide(input HMMCostGateInput) (sessionpin.Pin, planner.Decision, bool) {
	stayPin, ok := g.StayPin(input.Request, input.ActivePin, input.HMMHistory, input.Now)
	if !ok {
		return stayPin, planner.Decision{Outcome: planner.OutcomeSwitch, Reason: planner.ReasonNoPin}, false
	}
	phaseChanged := HMMToolExecutionPhaseChanged(stayPin.Reason, input.Fresh.Reason)

	cfg := input.Planner
	cfg.TierUpgradeEnabled = false
	stayPrefix, stayPrefixKnown := CacheablePrefixTokens(stayPin, input.EstimatedInputTokens, input.PrefixBroken)
	plan := planner.Decide(planner.Inputs{
		Pin:                   stayPin,
		Fresh:                 input.Fresh,
		EstimatedInputTokens:  input.EstimatedInputTokens,
		CacheablePrefixTokens: stayPrefix,
		CachePrefixKnown:      stayPrefixKnown,
		PriorOutputTokens:     stayPin.LastOutputTokens,
		AvailableModels:       g.AvailableModels,
		PinCacheCold:          PinCacheCold(stayPin, input.PrefixBroken, input.Now),
	}, cfg)

	freshIsMoreExpensive := HMMFreshIsMoreExpensive(stayPin.Model, input.Fresh.Model, input.EstimatedInputTokens)
	if phaseChanged && freshIsMoreExpensive {
		plan.Outcome = planner.OutcomeSwitch
		plan.Reason = HMMReasonPhaseChange
	} else if freshIsMoreExpensive {
		confidence, scored := HMMDecisionConfidence(input.Fresh)
		switch {
		case !scored:
			plan.Outcome = planner.OutcomeSwitch
			plan.Reason = HMMReasonUnscoredUpgrade
		case confidence >= g.UpgradeConfidenceThreshold:
			plan.Outcome = planner.OutcomeSwitch
			plan.Reason = HMMReasonConfidentUpgrade
		case plan.Outcome != planner.OutcomeSwitch:
			plan.Outcome = planner.OutcomeStay
			plan.Reason = HMMReasonUpgradeConfidenceLow
		}
	}

	if input.SameTierPin && plan.Outcome == planner.OutcomeSwitch && plan.Reason == planner.ReasonEVPositive {
		pinTier, freshTier := catalog.TierFor(stayPin.Model), catalog.TierFor(input.Fresh.Model)
		if pinTier != catalog.TierUnknown && pinTier == freshTier {
			plan.Outcome = planner.OutcomeStay
			plan.Reason = planner.ReasonSameTierPinned
		}
	}

	if phaseChanged && plan.Outcome == planner.OutcomeSwitch && plan.Reason == planner.ReasonEVPositive {
		plan.Reason = HMMReasonPhaseChange
	}

	return stayPin, plan, plan.Outcome == planner.OutcomeStay && stayPin.Model != input.Fresh.Model
}

func (g HMMCostGate) StayPin(req router.Request, activePin sessionpin.Pin, hmmHistory sessionpin.Pin, now time.Time) (sessionpin.Pin, bool) {
	var (
		best sessionpin.Pin
		ok   bool
	)
	if !IsHMMPinReason(activePin.Reason) {
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

func (g HMMCostGate) NormalizeStayPin(req router.Request, p sessionpin.Pin, now time.Time) (sessionpin.Pin, bool) {
	served := p.LastServedModel
	if served == "" {
		served = p.Model
	}
	effort, model := StripEffortSuffix(served)
	if model == "" {
		return sessionpin.Pin{}, false
	}
	if p.LastTurnEndedAt.IsZero() {
		return sessionpin.Pin{}, false
	}
	if !p.PinnedUntil.IsZero() && !p.PinnedUntil.After(now) {
		return sessionpin.Pin{}, false
	}
	if MaxedOutServedModel(p) != "" {
		return sessionpin.Pin{}, false
	}
	if _, excluded := req.ExcludedModels[model]; excluded {
		return sessionpin.Pin{}, false
	}
	if AutomaticallyDisabled(req, model) {
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

func IsHMMPinReason(reason string) bool {
	return reason == HMMHistoryReason ||
		strings.HasPrefix(strings.TrimSpace(reason), "hmm_policy")
}

func IsHMMToolExecutionReason(reason string) bool {
	return strings.HasPrefix(strings.TrimSpace(reason), "hmm_policy:tool_execution")
}

func HMMToolExecutionPhaseChanged(stayReason, freshReason string) bool {
	return IsHMMToolExecutionReason(stayReason) != IsHMMToolExecutionReason(freshReason)
}

func HMMDecisionConfidence(dec router.Decision) (float64, bool) {
	if dec.Metadata == nil {
		return 0, false
	}
	confidence := float64(dec.Metadata.ChosenScore)
	if confidence <= 0 {
		return 0, false
	}
	return confidence, true
}

func HMMFreshIsMoreExpensive(stayModel, freshModel string, inputTokens int) bool {
	stay, okStay := HMMEffectiveInputUSDPer1M(stayModel, inputTokens)
	fresh, okFresh := HMMEffectiveInputUSDPer1M(freshModel, inputTokens)
	return okStay && okFresh && fresh > stay
}

func HMMEffectiveInputUSDPer1M(model string, inputTokens int) (float64, bool) {
	price, ok := catalog.PrimaryPriceFor(model)
	if !ok {
		return 0, false
	}
	price = price.ForInputTokens(inputTokens)
	return price.InputUSDPer1M, true
}

func CacheablePrefixTokens(pin sessionpin.Pin, total int, prefixBroken bool) (int, bool) {
	if prefixBroken {
		return 0, true
	}
	cached := pin.LastCachedReadTokens + pin.LastCachedWriteTokens
	prior := PriorPromptTokens(pin)
	if prior <= 0 {
		return 0, false
	}
	share := min(1.0, float64(cached)/float64(prior))
	return int(share * float64(total)), true
}

func PriorPromptTokens(pin sessionpin.Pin) int {
	if providers.FamilyFor(pin.Provider) == providers.FamilyAnthropic {
		return pin.LastInputTokens + pin.LastCachedReadTokens + pin.LastCachedWriteTokens
	}
	return pin.LastInputTokens
}

func CacheWarm(pin sessionpin.Pin, now time.Time) bool {
	if pin.LastTurnEndedAt.IsZero() {
		return false
	}
	return now.Sub(pin.LastTurnEndedAt) < providers.CacheTTLFor(pin.Provider)
}

func PinCacheCold(pin sessionpin.Pin, prefixBroken bool, now time.Time) bool {
	return !CacheWarm(pin, now) || prefixBroken
}

func AutomaticallyDisabled(req router.Request, model string) bool {
	_, disabled := req.AutomaticExcludedModels[model]
	return disabled
}

func MaxedOutServedModel(pin sessionpin.Pin) string {
	if pin.LastOutputLimitAt.IsZero() || !pin.LastOutputLimitAt.Equal(pin.LastTurnEndedAt) {
		return ""
	}
	model := pin.LastServedModel
	if model == "" {
		model = pin.Model
	}
	return BaseModelOf(model)
}

func BaseModelOf(servedIdentity string) string {
	if idx := strings.LastIndex(servedIdentity, ":"); idx > 0 {
		return servedIdentity[:idx]
	}
	return servedIdentity
}

func StripEffortSuffix(model string) (effort string, modelOut string) {
	const sep = ":"
	idx := strings.LastIndex(model, sep)
	if idx < 0 || idx == len(model)-1 {
		return "", model
	}
	tail := strings.TrimSpace(model[idx+1:])
	if !LooksLikeEffortAlias(tail) {
		return "", model
	}
	return router.CanonicalizeEffort(tail), model[:idx]
}

func LooksLikeEffortAlias(tail string) bool {
	switch strings.ToLower(strings.TrimSpace(tail)) {
	case "fast", "low", "medium", "med", "high", "max", "xhigh",
		"ultra", "minimal", "min":
		return true
	default:
		return false
	}
}
