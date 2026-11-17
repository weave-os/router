package proxy

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions/entitlement"
)

var ErrSubscriptionUnavailableForTurn = errors.New("subscription unavailable and paid fallback not permitted for this turn")

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

type subscriptionPoolVerdict struct {
	refused  subscriptionlane.State
	sentinel error
}

var subscriptionPoolVerdicts = map[subscriptionlane.Event]subscriptionPoolVerdict{
	subscriptionlane.SubscriptionPoolExhausted:   {refused: subscriptionlane.RefusedSubscriptionPoolExhausted, sentinel: ErrSubscriptionPoolExhausted},
	subscriptionlane.SubscriptionPoolUnavailable: {refused: subscriptionlane.RefusedSubscriptionPoolUnavailable, sentinel: ErrSubscriptionPoolUnavailable},
	subscriptionlane.SubscriptionPoolThrottled:   {refused: subscriptionlane.RefusedSubscriptionPoolThrottled, sentinel: ErrSubscriptionPoolThrottled},
	subscriptionlane.CallerPlanThrottled:         {refused: subscriptionlane.RefusedSubscriptionPoolThrottled, sentinel: errCallerPlanThrottled},
}

func subscriptionPoolRefusal(ctx context.Context, on subscriptionlane.Event) error {
	verdict := subscriptionPoolVerdicts[on]
	if next, _ := subscriptionlane.Step(billing.SubscriptionLaneFromContext(ctx), on); next == verdict.refused {
		return verdict.sentinel
	}
	return errors.Join(subscriptionOnlyUnavailable(ctx), verdict.sentinel)
}

func subscriptionPoolUnavailableBecause(ctx context.Context, reason string) error {
	return fmt.Errorf("%w: %s", subscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolUnavailable), reason)
}

func subscriptionPoolExhausted(ctx context.Context) error {
	return subscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolExhausted)
}

func subscriptionLaneRefused(err error) bool {
	for _, gate := range subscriptionLaneOrgGates {
		if errors.Is(err, gate) {
			return true
		}
	}
	return false
}
