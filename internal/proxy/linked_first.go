package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
)

func linkedFirst(ctx context.Context) bool {
	reason, ok := billing.SubscriptionOnlyReasonFromContext(ctx)
	return ok && reason == billing.SubscriptionOnlyLinkedFirst
}

func paidFallbackForbidden(ctx context.Context) bool {
	return claudeEnterpriseTurn(ctx) && !claudeEnterpriseCapRescue(ctx) || pickedModelOnOwnPlan(ctx) || subscriptionAttemptOnly(ctx) || subscriptionStateModelsEnabled(ctx) && !subscriptionAPIOnly(ctx) || billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx)
}

func paidFallbackForbiddenForModel(ctx context.Context, model string) bool {
	if claudeEnterpriseTarget(ctx, inference.Target{CatalogID: model}) || pickedModelOnOwnPlan(ctx) || subscriptionAttemptOnly(ctx) || billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx) {
		return true
	}
	if !subscriptionStateModelsEnabled(ctx) || subscriptionAPIOnly(ctx) {
		return false
	}
	_, exhaustedModel := modelSet(installationSubscriptionModelsWhenInactiveFromContext(ctx))[model]
	return !exhaustedModel
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
