package proxy

import (
	"context"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
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

func strategyContext(ctx context.Context, strategy router.Strategy) context.Context {
	return router.WithStrategy(context.WithoutCancel(ctx), strategy)
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

func isUserForcedReason(reason string) bool {
	return strings.HasPrefix(reason, translate.ReasonUserForceModel)
}
