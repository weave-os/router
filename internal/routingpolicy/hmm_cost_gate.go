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

// HMMCostGate decides whether an HMM turn stays on the session's prior model
// or switches to the policy's fresh pick.
type HMMCostGate struct {
	Planner                    planner.EVConfig
	AvailableModels            map[string]struct{}
	RegisteredProviders        func() map[string]struct{}
	UpgradeConfidenceThreshold float64
	SameTierPin                bool
}

// HMMHistoryReason marks the HMM history row that outlives the session pin.
const HMMHistoryReason = "hmm_history"

// HMMReason* are the planner reasons the HMM cost gate records on its verdict.
const (
	HMMReasonConfidentUpgrade     = "hmm_confident_upgrade"
	HMMReasonUpgradeConfidenceLow = "hmm_upgrade_confidence_low"
	HMMReasonPhaseChange          = "hmm_phase_change"
)

// Decide reports the pin this HMM turn should stay on (sticky=true) or the
// prior model it switched away from, plus the planner's verdict.
func (g HMMCostGate) Decide(
	req router.Request,
	activePin sessionpin.Pin,
	hmmHistory sessionpin.Pin,
	fresh router.Decision,
	estimatedInputTokens int,
	prefixBroken bool,
) (sessionpin.Pin, planner.Decision, bool) {
	stayPin, ok := g.StayPin(req, activePin, hmmHistory)
	if !ok {
		return stayPin, planner.Decision{Outcome: planner.OutcomeSwitch, Reason: planner.ReasonNoPin}, false
	}
	if HMMToolExecutionPhaseChanged(stayPin.Reason, fresh.Reason) {
		return stayPin, planner.Decision{Outcome: planner.OutcomeSwitch, Reason: HMMReasonPhaseChange}, false
	}

	cfg := g.Planner
	// HMM owns semantic selection; the shared Go planner owns cache economics
	// because HMM clusters and catalog tiers are not the same axis.
	cfg.TierUpgradeEnabled = false
	stayPrefix, stayPrefixKnown := CacheablePrefixTokens(stayPin, estimatedInputTokens, prefixBroken)
	base := planner.Decide(planner.Inputs{
		Pin:                   stayPin,
		Fresh:                 fresh,
		EstimatedInputTokens:  estimatedInputTokens,
		CacheablePrefixTokens: stayPrefix,
		CachePrefixKnown:      stayPrefixKnown,
		PriorOutputTokens:     stayPin.LastOutputTokens,
		AvailableModels:       g.AvailableModels,
		PinCacheCold:          PinCacheCold(stayPin, prefixBroken),
	}, cfg)

	if HMMFreshIsMoreExpensive(stayPin.Model, fresh.Model, estimatedInputTokens) {
		confidence, ok := HMMDecisionConfidence(fresh)
		if ok && confidence >= g.UpgradeConfidenceThreshold {
			base.Outcome = planner.OutcomeSwitch
			base.Reason = HMMReasonConfidentUpgrade
		} else if base.Outcome != planner.OutcomeSwitch {
			base.Outcome = planner.OutcomeStay
			base.Reason = HMMReasonUpgradeConfidenceLow
		}
	}

	// Runs after the upgrade block, so only a plain ReasonEVPositive switch
	// reaches here; a confident upgrade has already changed Reason.
	if g.SameTierPin && base.Outcome == planner.OutcomeSwitch && base.Reason == planner.ReasonEVPositive {
		pinTier, freshTier := catalog.TierFor(stayPin.Model), catalog.TierFor(fresh.Model)
		if pinTier != catalog.TierUnknown && pinTier == freshTier {
			base.Outcome = planner.OutcomeStay
			base.Reason = planner.ReasonSameTierPinned
		}
	}

	if base.Outcome == planner.OutcomeStay && stayPin.Model != fresh.Model {
		return stayPin, base, true
	}
	return stayPin, base, false
}

// StayPin returns the most recent eligible HMM stay candidate among the
// active pin and the HMM history row.
func (g HMMCostGate) StayPin(req router.Request, activePin sessionpin.Pin, hmmHistory sessionpin.Pin) (sessionpin.Pin, bool) {
	var (
		best sessionpin.Pin
		ok   bool
	)
	// Only HMM-written pins are stay candidates; a cluster/planner pin from a
	// prior non-HMM stretch must not steer an HMM EV stay.
	if !IsHMMPinReason(activePin.Reason) {
		activePin = sessionpin.Pin{}
	}
	for _, candidate := range []sessionpin.Pin{activePin, hmmHistory} {
		normalized, candidateOK := g.NormalizeStayPin(req, candidate)
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

// NormalizeStayPin validates p as a stay candidate for req and re-resolves its
// provider binding when the stored one is no longer usable.
func (g HMMCostGate) NormalizeStayPin(req router.Request, p sessionpin.Pin) (sessionpin.Pin, bool) {
	model := p.LastServedModel
	if model == "" {
		model = p.Model
	}
	if model == "" {
		return sessionpin.Pin{}, false
	}
	if p.LastTurnEndedAt.IsZero() {
		return sessionpin.Pin{}, false
	}
	if !p.PinnedUntil.IsZero() && !p.PinnedUntil.After(time.Now()) {
		return sessionpin.Pin{}, false
	}
	if MaxedOutServedModel(p) != "" {
		return sessionpin.Pin{}, false
	}
	if req.ExcludedModels != nil {
		if _, excluded := req.ExcludedModels[model]; excluded {
			return sessionpin.Pin{}, false
		}
	}
	// An HMM stay is the policy re-choosing the incumbent, so a deployment-wide
	// disable applies to it exactly as it does to a fresh selection.
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
	providerSet := req.EnabledProviders
	if providerSet == nil {
		providerSet = g.RegisteredProviders()
	}
	// A failed turn preserves the prior model but may leave an invalid provider
	// binding; validate before reusing, or re-resolve against available providers.
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

// IsHMMPinReason reports whether reason is HMM-written (hmm_history or hmm_policy*);
// guards against a stale cluster/planner pin steering an HMM turn's EV stay.
func IsHMMPinReason(reason string) bool {
	return reason == HMMHistoryReason ||
		strings.HasPrefix(strings.TrimSpace(reason), "hmm_policy")
}

// IsHMMToolExecutionReason reports whether reason marks an HMM tool-execution turn.
func IsHMMToolExecutionReason(reason string) bool {
	return strings.HasPrefix(strings.TrimSpace(reason), "hmm_policy:tool_execution")
}

// HMMToolExecutionPhaseChanged reports whether the session entered or left a
// tool-execution stretch between the stay pin and the fresh pick.
func HMMToolExecutionPhaseChanged(stayReason, freshReason string) bool {
	return IsHMMToolExecutionReason(stayReason) != IsHMMToolExecutionReason(freshReason)
}

// HMMDecisionConfidence returns the policy's confidence in dec, if it reported one.
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

// HMMFreshIsMoreExpensive reports whether freshModel's input price exceeds
// stayModel's at this prompt size.
func HMMFreshIsMoreExpensive(stayModel, freshModel string, inputTokens int) bool {
	stay, okStay := HMMEffectiveInputUSDPer1M(stayModel, inputTokens)
	fresh, okFresh := HMMEffectiveInputUSDPer1M(freshModel, inputTokens)
	return okStay && okFresh && fresh > stay
}

// HMMEffectiveInputUSDPer1M is model's primary input price at this prompt size.
func HMMEffectiveInputUSDPer1M(model string, inputTokens int) (float64, bool) {
	price, ok := catalog.PrimaryPriceFor(model)
	if !ok {
		return 0, false
	}
	price = price.ForInputTokens(inputTokens)
	value := price.InputUSDPer1M
	return value, true
}

// CacheablePrefixTokens projects the pin's previous-turn cache-hit share
// onto this turn's prompt. The share is a ratio of two measured counters, not
// measured cached tokens over an estimated current total — the latter biases k
// toward 1. Reports false when the pin carries no usage telemetry.
func CacheablePrefixTokens(pin sessionpin.Pin, total int, prefixBroken bool) (int, bool) {
	if prefixBroken {
		return 0, true // a client trim really did evict the prefix
	}
	cached := pin.LastCachedReadTokens + pin.LastCachedWriteTokens
	prior := PriorPromptTokens(pin)
	if prior <= 0 {
		return 0, false
	}
	share := min(1.0, float64(cached)/float64(prior))
	return int(share * float64(total)), true
}

// PriorPromptTokens is the pin's previous-turn prompt size. Usage is
// extracted by wire family: input_tokens is fresh-only on the Anthropic family
// (disjoint from read/write) but is prompt_tokens — already cache-inclusive —
// everywhere else.
func PriorPromptTokens(pin sessionpin.Pin) int {
	if providers.FamilyFor(pin.Provider) == providers.FamilyAnthropic {
		return pin.LastInputTokens + pin.LastCachedReadTokens + pin.LastCachedWriteTokens
	}
	return pin.LastInputTokens
}

// PinCacheCold keeps ordinary and HMM paths on the same cache-economics rule.
func PinCacheCold(pin sessionpin.Pin, prefixBroken bool) bool {
	return !CacheWarm(pin) || prefixBroken
}

// CacheWarm reports whether the pin's upstream prompt cache is still warm
// (prior turn ended within the provider's cache TTL). Cold pins get no
// cache-read discount in the planner's EV math.
func CacheWarm(pin sessionpin.Pin) bool {
	if pin.LastTurnEndedAt.IsZero() {
		return false
	}
	return time.Since(pin.LastTurnEndedAt) < providers.CacheTTLFor(pin.Provider)
}

// AutomaticallyDisabled reports whether a deployment-wide disable withdrew this
// model from automatic selection. Every reuse of an automatically-chosen model
// consults it, so a disable takes effect on sessions already pinned to that
// model instead of only on sessions that route fresh.
func AutomaticallyDisabled(req router.Request, model string) bool {
	_, disabled := req.AutomaticExcludedModels[model]
	return disabled
}

// MaxedOutServedModel returns the bare model that hit its output limit on the
// pin's last turn, or "" when that turn finished normally.
func MaxedOutServedModel(pin sessionpin.Pin) string {
	if pin.LastOutputLimitAt.IsZero() || !pin.LastOutputLimitAt.Equal(pin.LastTurnEndedAt) {
		return ""
	}
	model := pin.LastServedModel
	if model == "" {
		model = pin.Model
	}
	// ExcludedModels / SafetyExcludedModels key on bare catalog IDs; strip effort
	// so the exclusion matches. Exclusion is model-level: saturate at any effort
	// and the model is barred at every effort for this turn.
	return BaseModelOf(model)
}

// BaseModelOf strips a trailing ":effort" from a serving identity, returning the
// bare catalog ID. Safe on inputs that carry no effort.
func BaseModelOf(servedIdentity string) string {
	if idx := strings.LastIndex(servedIdentity, ":"); idx > 0 {
		return servedIdentity[:idx]
	}
	return servedIdentity
}
