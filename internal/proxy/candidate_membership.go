package proxy

import (
	"context"
	"fmt"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func candidateRefusal(ctx context.Context, strategies map[router.Strategy]registeredStrategy, strategy router.Strategy, req router.Request, decision router.Decision) error {
	if req.Candidates.PassesHardFilters(decision.Model) {
		return nil
	}
	observability.FromContext(ctx).Error("Strategy returned a model outside the filtered candidate set",
		"strategy", strategy,
		"returned_model", decision.Model,
		"returned_provider", decision.Provider,
	)
	return fmt.Errorf("strategy %q returned model %q outside the filtered candidate set: %w: %w", strategy, decision.Model, policy.ErrNoEligibleArm, strategyUnavailable(strategies, strategy))
}

func strategyUnavailable(strategies map[router.Strategy]registeredStrategy, strategy router.Strategy) error {
	if registered, ok := strategies[strategy]; ok && registered.unavailable != nil {
		return registered.unavailable
	}
	return defaultStrategyUnavailable(strategy)
}
