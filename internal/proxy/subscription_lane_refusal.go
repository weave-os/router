package proxy

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/subscriptionlane"
)

func paidCapacityUnfunded(ctx context.Context) bool {
	return !subscriptionlane.Permits(billing.SubscriptionLaneFromContext(ctx), subscriptionlane.OrganizationCreditsServed)
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
	return errors.Join(credentials.SubscriptionOnlyUnavailable(ctx), verdict.sentinel)
}

func subscriptionPoolUnavailableBecause(ctx context.Context, reason string) error {
	return fmt.Errorf("%w: %s", subscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolUnavailable), reason)
}

func subscriptionPoolExhausted(ctx context.Context) error {
	return subscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolExhausted)
}
