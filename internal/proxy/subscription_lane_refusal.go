package proxy

import (
	"context"
	"errors"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions/entitlement"
)

var subscriptionLaneOrgGates = map[refusal.Kind]error{
	refusal.CreditsExhausted:               ErrCreditsExhaustedSubscriptionUnavailable,
	refusal.SpendCapReached:                ErrSpendCapSubscriptionUnavailable,
	refusal.SubscriptionUnavailableForTurn: ErrSubscriptionUnavailableForTurn,
}

func linkedFirst(ctx context.Context) bool {
	return billing.SubscriptionLaneFromContext(ctx) == subscriptionlane.LinkedFirst
}

func paidCapacityUnfunded(ctx context.Context) bool {
	return !subscriptionlane.Permits(billing.SubscriptionLaneFromContext(ctx), subscriptionlane.OrganizationCreditsServed)
}

func subscriptionLaneRefusal(ctx context.Context, on subscriptionlane.Event) refusal.Kind {
	next, _ := subscriptionlane.Step(billing.SubscriptionLaneFromContext(ctx), on)
	kind, _ := next.Refusal()
	return kind
}

func subscriptionOnlyUnavailable(ctx context.Context) error {
	orgGate := subscriptionLaneOrgGates[subscriptionLaneRefusal(ctx, subscriptionlane.SubscriptionUnservable)]
	if window, spent := entitlement.SpentWindowFromContext(ctx); spent {
		return &IncludedAllowanceSpentError{Window: window, orgGate: orgGate}
	}
	return orgGate
}

func subscriptionPoolExhausted(ctx context.Context) error {
	if subscriptionLaneRefusal(ctx, subscriptionlane.SubscriptionPoolExhausted) == refusal.SubscriptionPoolExhausted {
		return ErrSubscriptionPoolExhausted
	}
	return errors.Join(subscriptionOnlyUnavailable(ctx), ErrSubscriptionPoolExhausted)
}

func subscriptionLaneRefused(err error) bool {
	for _, gate := range subscriptionLaneOrgGates {
		if errors.Is(err, gate) {
			return true
		}
	}
	return false
}
