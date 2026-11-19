package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

const hmmHistoryReason = routingpolicy.HMMHistoryReason

func hmmCostGateFor(s *Service) routingpolicy.HMMCostGate {
	return routingpolicy.HMMCostGate{
		AvailableModels:            s.availableModels,
		RegisteredProviders:        func() map[string]struct{} { return s.clients.NameSet() },
		UpgradeConfidenceThreshold: s.hmmUpgradeConfidenceThreshold,
	}
}

func hmmCostGatedDecision(ctx context.Context, s *Service, input routingpolicy.HMMCostGateInput) (router.Decision, planner.Decision, bool, string) {
	input.Planner = plannerConfigFor(ctx, s.planner)
	input.Now = s.clock.Now()
	stayPin, plan, sticky := hmmCostGateFor(s).Decide(input)
	if sticky {
		return pinDecision(stayPin), plan, true, stayPin.Model
	}
	return input.Fresh, plan, false, stayPin.Model
}

func hmmStayPin(s *Service, req router.Request, activePin sessionpin.Pin, hmmHistory sessionpin.Pin) (sessionpin.Pin, bool) {
	return hmmCostGateFor(s).StayPin(req, activePin, hmmHistory, s.clock.Now())
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

func cacheWarm(pin sessionpin.Pin, now time.Time) bool { return routingpolicy.CacheWarm(pin, now) }

func pinCacheCold(pin sessionpin.Pin, prefixBroken bool, now time.Time) bool {
	return routingpolicy.PinCacheCold(pin, prefixBroken, now)
}

func automaticallyDisabled(req router.Request, model string) bool {
	return candidates.AutomaticallyDisabled(req, model)
}

func maxedOutServedModel(pin sessionpin.Pin) string { return candidates.MaxedOutServedModel(pin) }

func baseModelOf(servedIdentity string) string { return candidates.BaseModelOf(servedIdentity) }

func stripEffortSuffix(model string) (effort string, modelOut string) {
	return routingpolicy.StripEffortSuffix(model)
}
