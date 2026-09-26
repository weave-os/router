package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
)

// linkedFirst reports whether the turn is subscription-only because the
// caller's linked plan pays by preference — the one reason that still has
// organization credits behind it.
func linkedFirst(ctx context.Context) bool {
	reason, ok := billing.SubscriptionOnlyReasonFromContext(ctx)
	return ok && reason == billing.SubscriptionOnlyLinkedFirst
}

// releaseLinkedFirstWhenPlanSpent drops a linked-first mark before routing when
// the caller's linked plan has bound its window and a Weave/BYOK key exists to
// serve the turn instead. Runs ahead of routing so the whole turn — candidate
// providers, failover, credentials — behaves as an ordinary credit-funded
// turn rather than being restricted to a lane that can't serve it and then
// refused as if credits were gone.
func (s *Service) releaseLinkedFirstWhenPlanSpent(ctx context.Context, headers http.Header, routePath string) context.Context {
	if !linkedFirst(ctx) {
		return ctx
	}
	var spent bool
	switch routePath {
	case routePathMessages:
		spent = s.claudeSubscriptionExhausted(ctx, headers)
	case routePathChatCompletions, routePathResponses:
		spent = s.codexSubscriptionExhausted(ctx, headers)
	}
	if !spent {
		return ctx
	}
	observability.FromContext(ctx).Info("Linked subscription plan window is spent; continuing on organization credits", "route_path", routePath)
	return billing.ReleaseLinkedFirst(ctx)
}

// releaseUnservableLinkedFirst drops a linked-first mark after routing when the
// turn did not resolve onto the caller's subscription (a force-model pin or
// hard-pin to an uncovered model, a managed pool with no seat). The balance
// gate already admitted a linked-first turn against organization credits, so
// serving it paid is funded; only a credits_depleted turn is refused.
func releaseUnservableLinkedFirst(ctx context.Context, decision router.Decision) (context.Context, bool) {
	if !linkedFirst(ctx) {
		return ctx, false
	}
	observability.FromContext(ctx).Info("Linked subscription cannot serve the routed model; continuing on organization credits",
		"decision_provider", decision.Provider, "decision_model", decision.Model)
	return billing.ReleaseLinkedFirst(ctx), true
}
