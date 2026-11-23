package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
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
	return !servingCommitted(ctx, paidFallbackBinding(ctx), payableServingEvent(ctx, paidFallbackEvent(credentials.UsableBYOK(ctx, provider))))
}

func paidAttemptForbidden(ctx context.Context, creds *requestcontext.Credentials) bool {
	return !servingCommitted(ctx, paidFallbackBinding(ctx), payableServingEvent(ctx, servingEvent(creds)))
}

func subscriptionOnlyUnservable(ctx context.Context, provider, model string) bool {
	return !credentials.ServedOnSubscription(ctx) && !servedOnUnfundedKey(ctx) && !credentials.ManagedSubscriptionCanServe(ctx, provider, model)
}

func paidFallbackForbiddenForModel(ctx context.Context, model string) bool {
	return paidFallbackForbiddenForModelOnProvider(ctx, "", model)
}

func paidFallbackForbiddenForModelOnProvider(ctx context.Context, provider, model string) bool {
	event := subscriptionlane.OrganizationCreditsServed
	if provider != "" && credentials.PaidCapacityUnfunded(ctx) {
		event = payableServingEvent(ctx, paidFallbackEvent(credentials.UsableBYOK(ctx, provider)))
	}
	return !servingCommitted(ctx, paidFallbackBindingForModel(ctx, model), event)
}

func subscriptionStatePaidRescueContext(ctx context.Context, model string) context.Context {
	if _, exhaustedModel := modelSet(installationSubscriptionModelsWhenInactiveFromContext(ctx))[model]; exhaustedModel {
		return context.WithValue(ctx, credentials.SubscriptionAPIOnlyKey{}, true)
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
	if !credentials.LinkedFirst(ctx) {
		return ctx, false
	}
	observability.FromContext(ctx).Info("Linked subscription cannot serve the routed model; continuing on organization credits",
		"decision_provider", decision.Provider, "decision_model", decision.Model)
	return billing.ReleaseLinkedFirst(ctx), true
}

func payableServingEvent(ctx context.Context, event subscriptionlane.Event) subscriptionlane.Event {
	if event == subscriptionlane.CustomerKeyServed && billing.ByokFeeUnpayable(ctx) {
		return subscriptionlane.OrganizationCreditsServed
	}
	return event
}

func servedOnUnfundedKey(ctx context.Context) bool {
	return credentials.PaidCapacityUnfunded(ctx) && credentials.UnfundedKeyFunds(ctx, credentials.CredentialsFromContext(ctx))
}
