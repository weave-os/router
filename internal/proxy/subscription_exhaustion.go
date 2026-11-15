package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
)

// claudeSubscriptionExhausted reports whether the caller's Claude subscription
// is exhausted or actively drawing billable overage. True only when a Claude
// token has an observed spent/billable snapshot and a paid fallback key exists.
// The token key is derived identically to withUsageObserver /
// usageBypassEngaged so this read agrees with what the observer recorded. When
// true the caller suppresses the subscription credential (withSuppressedSubscription)
// so the turn serves on the Weave / BYOK key rather than the customer's credits.
func (s *Service) claudeSubscriptionExhausted(ctx context.Context, headers http.Header) bool {
	return s.anthropicSubscriptionObservedExhausted(ctx, headers)
}

// anthropicSubscriptionObservedExhausted is credentials.ClaudeSubscriptionObservedExhausted
// for the caller's present Claude subscription. Subscription-only refusal uses
// this bare signal: an exhausted sub can only 429, so the turn is refused with
// a controlled 402.
func (s *Service) anthropicSubscriptionObservedExhausted(ctx context.Context, headers http.Header) bool {
	if s.usageObserver == nil {
		return false
	}
	_, anthroTok := presentSubscriptionTokens(ctx, headers)
	return credentials.ClaudeSubscriptionObservedExhausted(s.usageObserver, anthroTok, claudeSubscriptionFacts(ctx))
}

func claudeSnapshotUnservable(ctx context.Context, snap usage.Snapshot) bool {
	return credentials.ClaudeSnapshotUnservable(claudeSubscriptionFacts(ctx), snap)
}

func claudeSubscriptionFacts(ctx context.Context) credentials.ClaudeSubscriptionFacts {
	reason, subscriptionOnly := billing.SubscriptionOnlyReasonFromContext(ctx)
	return credentials.ClaudeSubscriptionFacts{
		EnterpriseOnly:       claudeEnterpriseOnly(ctx),
		PickedModelOnOwnPlan: pickedModelOnOwnPlan(ctx),
		EnterpriseSubscriptionOnly: subscriptionOnly && reason != billing.SubscriptionOnlyLinkedFirst &&
			claudeSubscriptionPlan(ctx) == providers.SubscriptionPlanEnterprise,
	}
}
