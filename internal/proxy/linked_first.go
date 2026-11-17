package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptionlane"
)

func paidFallbackForbidden(ctx context.Context) bool {
	return !servingCommitted(ctx, paidFallbackBinding(ctx), subscriptionlane.OrganizationCreditsServed)
}

func paidFallbackForbiddenByPlan(ctx context.Context) bool {
	return !paidFallbackBinding(ctx).Admits(subscriptionlane.OrganizationCreditsServed)
}

func paidFallbackForbiddenFor(ctx context.Context, provider string) bool {
	return !servingCommitted(ctx, paidFallbackBinding(ctx), paidFallbackEvent(usableBYOK(ctx, provider)))
}

func paidAttemptForbidden(ctx context.Context, creds *Credentials) bool {
	return !servingCommitted(ctx, paidFallbackBinding(ctx), servingEvent(creds))
}

func subscriptionOnlyUnservable(ctx context.Context, provider, model string) bool {
	return !servedOnSubscription(ctx) && !(paidCapacityUnfunded(ctx) && servedOnCustomerKey(ctx)) && !managedSubscriptionCanServe(ctx, provider, model)
}

func paidFallbackForbiddenForModel(ctx context.Context, model string) bool {
	return !servingCommitted(ctx, paidFallbackBindingForModel(ctx, model), subscriptionlane.OrganizationCreditsServed)
}

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

func releaseUnservableLinkedFirst(ctx context.Context, decision router.Decision) (context.Context, bool) {
	if !linkedFirst(ctx) {
		return ctx, false
	}
	observability.FromContext(ctx).Info("Linked subscription cannot serve the routed model; continuing on organization credits",
		"decision_provider", decision.Provider, "decision_model", decision.Model)
	return billing.ReleaseLinkedFirst(ctx), true
}

func releaseThrottledLinkedFirst(ctx context.Context) (context.Context, bool) {
	if !linkedFirst(ctx) {
		return ctx, false
	}
	observability.FromContext(ctx).Info("Linked subscription rejected the turn; rerouting on organization credits")
	return billing.ReleaseLinkedFirst(ctx), true
}
