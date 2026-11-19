package proxy

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
)

func logCandidateViolation(ctx context.Context, strategy router.Strategy, req router.Request, decision router.Decision) {
	violated, hasViolation := req.Candidates.HardFilterViolation(decision.Model)
	if !hasViolation {
		return
	}
	observability.FromContext(ctx).Warn("Strategy returned a model outside the filtered candidate set",
		"strategy", strategy,
		"model", decision.Model,
		"provider", decision.Provider,
		"violated_set", violated,
	)
}
