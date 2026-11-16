package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptionlane"
)

// paidFallbackForbidden reports whether a live subscription failure may not be
// rescued on metered capacity. A credits_depleted turn has nowhere to fall
// through to, and a picked model stays on the caller's own login. Configured
// subscription sets also forbid the ordinary same-model paid rescue; their
// dispatcher separately authorizes the exhausted model set.
// Enterprise-only Claude turns also forbid metered recovery after a refusal,
// except on the customer's plan-first key once the seat hit its overage cap.
func paidFallbackForbidden(ctx context.Context) bool {
	return !servingCommitted(ctx, paidFallbackBinding(ctx), subscriptionlane.OrganizationCreditsServed)
}

// paidFallbackForbiddenByPlan is paidFallbackForbidden without the unfunded
// organization: these turns stay on the caller's subscription even when the
// customer holds a provider key.
func paidFallbackForbiddenByPlan(ctx context.Context) bool {
	return !paidFallbackBinding(ctx).Admits(subscriptionlane.OrganizationCreditsServed)
}

// paidFallbackForbiddenFor is paidFallbackForbidden for a fallback onto
// provider: an unfunded organization may still fall back to the customer's
// own usable key for provider.
func paidFallbackForbiddenFor(ctx context.Context, provider string) bool {
	return !servingCommitted(ctx, paidFallbackBinding(ctx), paidFallbackEvent(usableBYOK(ctx, provider)))
}

// paidAttemptForbidden reports whether dispatch must refuse an attempt on
// creds. An unfunded organization's turn may still run on the customer's own
// usable provider key.
func paidAttemptForbidden(ctx context.Context, creds *Credentials) bool {
	return !servingCommitted(ctx, paidFallbackBinding(ctx), servingEvent(creds))
}

// subscriptionOnlyUnservable reports whether a subscription-only turn routed
// to provider/model has no customer-funded credential to serve it: not the
// caller's plan (direct or managed) and, for an unfunded organization, not the
// customer's own provider key.
func subscriptionOnlyUnservable(ctx context.Context, provider, model string) bool {
	return !servedOnSubscription(ctx) && !(paidCapacityUnfunded(ctx) && servedOnCustomerKey(ctx)) && !managedSubscriptionCanServe(ctx, provider, model)
}

func paidFallbackForbiddenForModel(ctx context.Context, model string) bool {
	return !servingCommitted(ctx, paidFallbackBindingForModel(ctx, model), subscriptionlane.OrganizationCreditsServed)
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
// which is usually the first exhaustion signal, or a model-access or
// third-party-client refusal the bypass reroutes — so the reroute that follows
// runs on organization credits instead of being refused as if they were gone.
func releaseThrottledLinkedFirst(ctx context.Context) (context.Context, bool) {
	if !linkedFirst(ctx) {
		return ctx, false
	}
	observability.FromContext(ctx).Info("Linked subscription rejected the turn; rerouting on organization credits")
	return billing.ReleaseLinkedFirst(ctx), true
}
