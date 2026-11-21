package proxy

import (
	"context"
	"fmt"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
)

func eligibleStrategyDecision(ctx context.Context, strategy router.Strategy, req router.Request, decision router.Decision) (router.Decision, error) {
	if err := productEligibilityRefusal(ctx, req.ProductEligibility, decision.Model); err != nil {
		return router.Decision{}, fmt.Errorf("strategy %q: %w", strategy, err)
	}
	return candidates.EnforceMembership(ctx, strategy, req, decision)
}
