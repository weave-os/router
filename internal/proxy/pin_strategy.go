package proxy

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

func pinMatchesEffectiveStrategy(ctx context.Context, pin sessionpin.Pin) bool {
	if isUserForcedReason(pin.Reason) {
		return true
	}
	expected := router.StrategyFromContext(ctx)
	if pin.Strategy == expected {
		return true
	}
	return pin.Strategy == "" && !router.IsBetaStrategy(expected)
}

func strategyContext(strategy router.Strategy) context.Context {
	return router.WithStrategy(context.Background(), strategy)
}

func strategyForTurnLoopResult(res turnLoopResult) router.Strategy {
	if res.Strategy != "" {
		return res.Strategy
	}
	for _, decision := range []router.Decision{res.Decision, res.Fresh} {
		if decision.Metadata != nil && decision.Metadata.Strategy != "" {
			return router.Strategy(decision.Metadata.Strategy)
		}
	}
	return router.StrategyHMMEmbedding
}
