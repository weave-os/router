package proxy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

const hmmHistoryReason = routingpolicy.HMMHistoryReason

func hmmCostGateFor(s *Service) routingpolicy.HMMCostGate {
	return routingpolicy.HMMCostGate{
		Planner:                    s.planner,
		AvailableModels:            s.availableModels,
		RegisteredProviders:        func() map[string]struct{} { return s.clients.NameSet() },
		UpgradeConfidenceThreshold: s.hmmUpgradeConfidenceThreshold,
		SameTierPin:                s.hmmSameTierPin,
	}
}

func hmmCostGatedDecision(
	s *Service,
	req router.Request,
	activePin sessionpin.Pin,
	hmmHistory sessionpin.Pin,
	fresh router.Decision,
	estimatedInputTokens int,
	prefixBroken bool,
) (router.Decision, planner.Decision, bool, string) {
	stayPin, plan, sticky := hmmCostGateFor(s).Decide(req, activePin, hmmHistory, fresh, estimatedInputTokens, prefixBroken)
	if sticky {
		return pinDecision(stayPin), plan, true, stayPin.Model
	}
	return fresh, plan, false, stayPin.Model
}

func hmmFreshIsMoreExpensive(stayModel, freshModel string, inputTokens int) bool {
	return routingpolicy.HMMFreshIsMoreExpensive(stayModel, freshModel, inputTokens)
}

func hmmEffectiveInputUSDPer1M(model string, inputTokens int) (float64, bool) {
	return routingpolicy.HMMEffectiveInputUSDPer1M(model, inputTokens)
}

func cacheablePrefixTokens(pin sessionpin.Pin, total int, prefixBroken bool) (int, bool) {
	return routingpolicy.CacheablePrefixTokens(pin, total, prefixBroken)
}

func priorPromptTokens(pin sessionpin.Pin) int { return routingpolicy.PriorPromptTokens(pin) }

func pinCacheCold(pin sessionpin.Pin, prefixBroken bool) bool {
	return routingpolicy.PinCacheCold(pin, prefixBroken)
}

func automaticallyDisabled(req router.Request, model string) bool {
	return routingpolicy.AutomaticallyDisabled(req, model)
}

func maxedOutServedModel(pin sessionpin.Pin) string { return routingpolicy.MaxedOutServedModel(pin) }

func baseModelOf(servedIdentity string) string { return routingpolicy.BaseModelOf(servedIdentity) }
