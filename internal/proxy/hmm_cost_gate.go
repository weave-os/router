package proxy

import (
	"context"
	"strings"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

func (s *Service) hmmCostGatedDecision(
	ctx context.Context, req router.Request,
	activePin sessionpin.Pin,
	hmmHistory sessionpin.Pin,
	fresh router.Decision,
	estimatedInputTokens int,
	prefixBroken bool,
) (router.Decision, planner.Decision, bool, string) {
	stayPin, ok := s.hmmStayPin(req, activePin, hmmHistory)
	if !ok {
		return fresh, planner.Decision{Outcome: planner.OutcomeSwitch, Reason: planner.ReasonNoPin}, false, ""
	}
	phaseChanged := hmmToolExecutionPhaseChanged(stayPin.Reason, fresh.Reason)

	cfg := plannerConfigFor(ctx, s.planner)
	cfg.TierUpgradeEnabled = false
	stayPrefix, stayPrefixKnown := cacheablePrefixTokens(stayPin, estimatedInputTokens, prefixBroken)
	base := planner.Decide(planner.Inputs{
		Pin:                   stayPin,
		Fresh:                 fresh,
		EstimatedInputTokens:  estimatedInputTokens,
		CacheablePrefixTokens: stayPrefix,
		CachePrefixKnown:      stayPrefixKnown,
		PriorOutputTokens:     stayPin.LastOutputTokens,
		AvailableModels:       s.availableModels,
		PinCacheCold:          pinCacheCold(stayPin, prefixBroken, s.clock.Now()),
	}, cfg)

	freshIsMoreExpensive := hmmFreshIsMoreExpensive(stayPin.Model, fresh.Model, estimatedInputTokens)
	if phaseChanged && freshIsMoreExpensive {
		base.Outcome = planner.OutcomeSwitch
		base.Reason = hmmReasonPhaseChange
	} else if freshIsMoreExpensive {
		confidence, ok := hmmDecisionConfidence(fresh)
		if ok && confidence >= s.hmmUpgradeConfidenceThreshold {
			base.Outcome = planner.OutcomeSwitch
			base.Reason = hmmReasonConfidentUpgrade
		} else if base.Outcome != planner.OutcomeSwitch {
			base.Outcome = planner.OutcomeStay
			base.Reason = hmmReasonUpgradeConfidenceLow
		}
	}

	if flags.BoolOr(ctx, flags.KeyHMMSameTierPin, s.hmmSameTierPin) && base.Outcome == planner.OutcomeSwitch && base.Reason == planner.ReasonEVPositive {
		pinTier, freshTier := catalog.TierFor(stayPin.Model), catalog.TierFor(fresh.Model)
		if pinTier != catalog.TierUnknown && pinTier == freshTier {
			base.Outcome = planner.OutcomeStay
			base.Reason = planner.ReasonSameTierPinned
		}
	}

	if phaseChanged && base.Outcome == planner.OutcomeSwitch && base.Reason == planner.ReasonEVPositive {
		base.Reason = hmmReasonPhaseChange
	}

	if base.Outcome == planner.OutcomeStay && stayPin.Model != fresh.Model {
		return pinDecision(stayPin), base, true, stayPin.Model
	}
	return fresh, base, false, stayPin.Model
}

func (s *Service) hmmStayPin(req router.Request, activePin sessionpin.Pin, hmmHistory sessionpin.Pin) (sessionpin.Pin, bool) {
	var (
		best sessionpin.Pin
		ok   bool
	)
	if !isHMMPinReason(activePin.Reason) {
		activePin = sessionpin.Pin{}
	}
	for _, candidate := range []sessionpin.Pin{activePin, hmmHistory} {
		normalized, candidateOK := s.normalizeHMMStayPin(req, candidate)
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

func isHMMPinReason(reason string) bool {
	return reason == hmmHistoryReason ||
		strings.HasPrefix(strings.TrimSpace(reason), "hmm_policy")
}

func isHMMToolExecutionReason(reason string) bool {
	return strings.HasPrefix(strings.TrimSpace(reason), "hmm_policy:tool_execution")
}

func hmmToolExecutionPhaseChanged(stayReason, freshReason string) bool {
	return isHMMToolExecutionReason(stayReason) != isHMMToolExecutionReason(freshReason)
}

func (s *Service) normalizeHMMStayPin(req router.Request, p sessionpin.Pin) (sessionpin.Pin, bool) {
	served := p.LastServedModel
	if served == "" {
		served = p.Model
	}
	effort, model := stripEffortSuffix(served)
	if model == "" {
		return sessionpin.Pin{}, false
	}
	if p.LastTurnEndedAt.IsZero() {
		return sessionpin.Pin{}, false
	}
	if !p.PinnedUntil.IsZero() && !p.PinnedUntil.After(s.clock.Now()) {
		return sessionpin.Pin{}, false
	}
	if maxedOutServedModel(p) != "" {
		return sessionpin.Pin{}, false
	}
	if _, excluded := req.ExcludedModels[model]; excluded {
		return sessionpin.Pin{}, false
	}
	if automaticallyDisabled(req, model) {
		return sessionpin.Pin{}, false
	}
	if s.availableModels != nil {
		if _, available := s.availableModels[model]; !available {
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
		providerSet = s.clients.NameSet()
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

func hmmDecisionConfidence(dec router.Decision) (float64, bool) {
	if dec.Metadata == nil {
		return 0, false
	}
	confidence := float64(dec.Metadata.ChosenScore)
	if confidence <= 0 {
		return 0, false
	}
	return confidence, true
}

func hmmFreshIsMoreExpensive(stayModel, freshModel string, inputTokens int) bool {
	stay, okStay := hmmEffectiveInputUSDPer1M(stayModel, inputTokens)
	fresh, okFresh := hmmEffectiveInputUSDPer1M(freshModel, inputTokens)
	return okStay && okFresh && fresh > stay
}

func hmmEffectiveInputUSDPer1M(model string, inputTokens int) (float64, bool) {
	price, ok := catalog.PrimaryPriceFor(model)
	if !ok {
		return 0, false
	}
	price = price.ForInputTokens(inputTokens)
	return price.InputUSDPer1M, true
}
