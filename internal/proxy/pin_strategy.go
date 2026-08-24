package proxy

import (
	"context"

	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionpin"
)

// pinMatchesEffectiveStrategy reports whether a stored pin belongs to the
// strategy serving this request. Legacy (empty Strategy) rows remain eligible
// for non-beta strategies during rollout; beta never inherits a legacy pin.
func pinMatchesEffectiveStrategy(ctx context.Context, pin sessionpin.Pin) bool {
	expected := router.StrategyFromContext(ctx)
	if pin.Strategy == expected {
		return true
	}
	return pin.Strategy == "" && expected != router.StrategyHMMBeta
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
	return router.StrategyCluster
}
