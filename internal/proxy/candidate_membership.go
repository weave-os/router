package proxy

import (
	"context"
	"fmt"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

func enforceCandidateMembership(ctx context.Context, strategy router.Strategy, req router.Request, decision router.Decision) (router.Decision, error) {
	violated, hard := req.Candidates.HardFilterViolation(decision.Model)
	if !hard {
		if !req.Candidates.AutomaticallyExcludes(decision.Model) {
			return decision, nil
		}
		violated = router.ViolationAutomaticExcludedModels
	}
	fallback, found := router.EligibleRankedFallback(req, decision, func(model string) bool {
		return catalog.PermittedBy(req.ProductEligibility, model)
	})
	log := observability.FromContext(ctx)
	if !found && !hard {
		return decision, nil
	}
	if !found {
		log.Warn("Strategy returned a model outside the eligible candidates with no eligible ranked fallback",
			"strategy", strategy, "model", decision.Model, "provider", decision.Provider, "violated_set", violated)
		return router.Decision{}, fmt.Errorf("strategy %q returned %s outside %s: %w", strategy, decision.Model, violated, policy.ErrNoEligibleArm)
	}
	log.Warn("Strategy returned a model outside the eligible candidates; serving its first eligible ranked fallback",
		"strategy", strategy, "model", decision.Model, "provider", decision.Provider, "violated_set", violated,
		"fallback_model", fallback.Model, "fallback_provider", fallback.Provider)
	return fallback, nil
}
