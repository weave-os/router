package floor

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
)

func (h *Helper) Settle(ctx context.Context, t *Turn, decision router.Decision, servedGroup string, routeErr error) (committed Committed, rerunBaseline bool, err error) {
	committed, err = h.Finish(ctx, t, decision, servedGroup, routeErr)
	if err != nil {
		observability.FromContext(ctx).Warn("Escalation state commit failed; routing without intervention", "err", err, "ordinal", t.Ordinal())
		return Committed{}, RoutingApplied(decision), err
	}
	return committed, false, nil
}

func (h *Helper) SettleLLM(ctx context.Context, t *Turn, decision router.Decision, routeErr error) (rerunBaseline bool) {
	if !t.HasLLM() || routeErr != nil {
		return false
	}
	if err := h.ApplyLLM(ctx, t, decision); err != nil {
		observability.FromContext(ctx).Warn("LLM escalation application failed", "err", err)
		return t.LLMActive() && RoutingApplied(decision)
	}
	return false
}
