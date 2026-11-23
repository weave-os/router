package routingpolicy

import (
	"context"

	"weave-os/router/internal/router"
)

func StrategyContext(ctx context.Context, strategy router.Strategy) context.Context {
	return router.WithStrategy(context.WithoutCancel(ctx), strategy)
}

func StrategyForTurnRoute(res TurnRoute) router.Strategy {
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
