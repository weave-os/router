package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

// linkedFirst reports whether the turn is subscription-only because the
// caller's linked plan pays by preference — the one reason that still has
// organization credits behind it.
func linkedFirst(ctx context.Context) bool {
	reason, ok := billing.SubscriptionOnlyReasonFromContext(ctx)
	return ok && reason == billing.SubscriptionOnlyLinkedFirst
}

// paidFallbackForbidden reports whether a live subscription failure may not be
// rescued on metered capacity. Only a credits_depleted turn has nowhere to fall
// through to; a linked-first turn's organization credits are intact, so its
// plan throttling the turn rolls over the same way an observed-spent plan does.
func paidFallbackForbidden(ctx context.Context) bool {
	return billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx)
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
		spent = s.claudeSubscriptionExhausted(ctx, headers) || s.coveringManagedPoolSpent(ctx, subscriptions.ProviderClaude)
	case routePathChatCompletions, routePathResponses:
		spent = s.codexSubscriptionExhausted(ctx, headers) || s.coveringManagedPoolSpent(ctx, subscriptions.ProviderCodex)
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

// releaseThrottledLinkedFirst drops a linked-first mark when the caller's plan
// refused the turn live — a retryable 429 the observer had not yet recorded,
// which is usually the first exhaustion signal — so the reroute that follows
// runs on organization credits instead of being refused as if they were gone.
func releaseThrottledLinkedFirst(ctx context.Context) (context.Context, bool) {
	if !linkedFirst(ctx) {
		return ctx, false
	}
	observability.FromContext(ctx).Info("Linked subscription throttled the turn; rerouting on organization credits")
	return billing.ReleaseLinkedFirst(ctx), true
}

// coveringManagedPoolSpent reports whether this request is enrolled in a
// managed pool for provider whose every account is already exhausted, and a
// Weave/BYOK key exists to serve instead. Personal-OAuth exhaustion is handled
// by claudeSubscriptionExhausted / codexSubscriptionExhausted; this is the
// pool analogue so a linked-first turn whose covering seats are spent is
// released before routing rather than leased-and-refused.
func (s *Service) coveringManagedPoolSpent(ctx context.Context, provider subscriptions.Provider) bool {
	if !managedSubscriptionEnrolled(ctx, provider) {
		return false
	}
	states := managedSubscriptionPlanStatesFromContext(ctx)
	if states[provider] != SubscriptionPlanStateExhausted {
		return false
	}
	return s.managedProviderFallbackAvailable(ctx, provider)
}
