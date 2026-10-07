package proxy

import (
	"context"

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

// paidFallbackForbidden reports whether a live subscription failure may not be
// rescued on metered capacity. Only a credits_depleted turn has nowhere to fall
// through to. Configured subscription sets also forbid the ordinary same-model
// paid rescue; their dispatcher separately authorizes the exhausted model set.
func paidFallbackForbidden(ctx context.Context) bool {
	return subscriptionAttemptOnly(ctx) || subscriptionStateModelsEnabled(ctx) && !subscriptionAPIOnly(ctx) || billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx)
}

func paidFallbackForbiddenForModel(ctx context.Context, model string) bool {
	if subscriptionAttemptOnly(ctx) || billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx) {
		return true
	}
	if !subscriptionStateModelsEnabled(ctx) || subscriptionAPIOnly(ctx) {
		return false
	}
	_, exhaustedModel := modelSet(installationSubscriptionModelsWhenInactiveFromContext(ctx))[model]
	return !exhaustedModel
}

// subscriptionStatePaidRescueContext permits paid recovery only when its target
// is explicitly present in the installation's exhausted-state model set.
func subscriptionStatePaidRescueContext(ctx context.Context, model string) context.Context {
	if _, exhaustedModel := modelSet(installationSubscriptionModelsWhenInactiveFromContext(ctx))[model]; exhaustedModel {
		return context.WithValue(ctx, subscriptionAPIOnlyKey{}, true)
	}
	return ctx
}

func subscriptionStatePaidRescueDecisions(ctx context.Context, candidates []router.Decision) []router.Decision {
	if !subscriptionStateModelsEnabled(ctx) {
		return candidates
	}
	allowedDecisions := make([]router.Decision, 0, len(candidates))
	for _, candidate := range candidates {
		if !paidFallbackForbiddenForModel(ctx, candidate.Model) {
			allowedDecisions = append(allowedDecisions, candidate)
		}
	}
	return allowedDecisions
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
