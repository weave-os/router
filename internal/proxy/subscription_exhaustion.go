package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
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

// anthropicSubscriptionObservedExhausted reports whether the caller's present
// Claude subscription should not serve the turn per the observer (see
// claudeSnapshotUnservable), independent of whether a fallback key exists.
// Subscription-only refusal uses this bare signal: an exhausted sub can only
// 429, so the turn is refused with a controlled 402.
func (s *Service) anthropicSubscriptionObservedExhausted(ctx context.Context, headers http.Header) bool {
	if s.usageObserver == nil {
		return false
	}
	_, anthroTok := presentSubscriptionTokens(ctx, headers)
	if anthroTok == "" {
		return false
	}
	snap, ok := s.usageObserver.Snapshot(s.usageObserver.Key([]byte(anthroTok)))
	return ok && claudeSnapshotUnservable(ctx, snap)
}

// claudeSnapshotUnservable reports whether an observed Claude subscription
// should leave the turn. A usage-billed seat has no plan quota to exhaust, so
// once paid fallback is off its overage is the caller's only lane; plan
// overage still leaves the subscription. A picked model on the caller's own
// login lets Anthropic decide, as the native client would.
func claudeSnapshotUnservable(ctx context.Context, snap usage.Snapshot) bool {
	if claudeEnterpriseOnly(ctx) {
		return false
	}
	if pickedModelOnOwnPlan(ctx) {
		return false
	}
	if reason, ok := billing.SubscriptionOnlyReasonFromContext(ctx); ok && reason != billing.SubscriptionOnlyLinkedFirst && claudeSubscriptionPlan(ctx) == providers.SubscriptionPlanEnterprise {
		return false
	}
	return snap.BillableOrExhausted()
}
