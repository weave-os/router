package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
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
// rescued on metered capacity. A credits_depleted turn has nowhere to fall
// through to, and a picked model stays on the caller's own login. Configured
// subscription sets also forbid the ordinary same-model paid rescue; their
// dispatcher separately authorizes the exhausted model set.
// Enterprise-only Claude turns also forbid metered recovery after a refusal,
// except on the customer's plan-first key once the seat hit its overage cap.
func paidFallbackForbidden(ctx context.Context) bool {
	return paidFallbackForbiddenByPlan(ctx) || paidCapacityUnfunded(ctx)
}

// paidFallbackForbiddenByPlan is paidFallbackForbidden without the unfunded
// organization: these turns stay on the caller's subscription even when the
// customer holds a provider key.
func paidFallbackForbiddenByPlan(ctx context.Context) bool {
	return claudeEnterpriseTurn(ctx) && !claudeEnterpriseCapRescue(ctx) || pickedModelOnOwnPlan(ctx) || subscriptionAttemptOnly(ctx) || subscriptionStateModelsEnabled(ctx) && !subscriptionAPIOnly(ctx)
}

// paidCapacityUnfunded reports whether the organization cannot pay for the
// turn (credits depleted or a spend cap reached). Only customer-funded
// credentials may serve it; Weave's deployment keys never do.
func paidCapacityUnfunded(ctx context.Context) bool {
	return billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx)
}

// paidFallbackForbiddenFor is paidFallbackForbidden for a fallback onto
// provider: an unfunded organization may still fall back to the customer's
// own usable key for provider.
func paidFallbackForbiddenFor(ctx context.Context, provider string) bool {
	return paidFallbackForbiddenByPlan(ctx) || paidCapacityUnfunded(ctx) && usableBYOK(ctx, provider) == nil
}

// paidAttemptForbidden reports whether dispatch must refuse an attempt on
// creds. An unfunded organization's turn may still run on the customer's own
// usable provider key.
func paidAttemptForbidden(ctx context.Context, creds *Credentials) bool {
	if creds != nil && creds.OAuth {
		return false
	}
	return paidFallbackForbiddenByPlan(ctx) || paidCapacityUnfunded(ctx) && !customerKeyFunded(creds)
}

// subscriptionOnlyUnservable reports whether a subscription-only turn routed
// to provider/model has no customer-funded credential to serve it: not the
// caller's plan (direct or managed) and, for an unfunded organization, not the
// customer's own provider key.
func subscriptionOnlyUnservable(ctx context.Context, provider, model string) bool {
	return !servedOnSubscription(ctx) && !(paidCapacityUnfunded(ctx) && servedOnCustomerKey(ctx)) && !managedSubscriptionCanServe(ctx, provider, model)
}

func paidFallbackForbiddenForModel(ctx context.Context, model string) bool {
	return paidFallbackForbiddenForModelOnProvider(ctx, "", model)
}

// paidFallbackForbiddenForModelOnProvider is paidFallbackForbiddenForModel for a
// recovery onto provider: an unfunded organization's turn may still recover
// on the customer's own usable key for provider.
func paidFallbackForbiddenForModelOnProvider(ctx context.Context, provider, model string) bool {
	unfunded := paidCapacityUnfunded(ctx) && (provider == "" || usableBYOK(ctx, provider) == nil)
	if claudeEnterpriseTarget(ctx, inference.Target{CatalogID: model}) || pickedModelOnOwnPlan(ctx) || subscriptionAttemptOnly(ctx) || unfunded {
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

// claudeRetryRefusal reports whether a Claude subscription error on the Chat
// surfaces earns a paid retry: a model-access or third-party refusal, or, on an
// unfunded turn that can only recover on the customer's key, any live refusal.
func claudeRetryRefusal(ctx context.Context, err error) bool {
	return anthropicSubscriptionModelRejected(err) || anthropicSubscriptionThirdPartyRefused(err) ||
		paidCapacityUnfunded(ctx) && (providers.IsRetryable(err) || anthropicOAuthCredentialRejected(err))
}
