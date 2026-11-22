package credentials

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions"
)

var (
	ErrSubscriptionPoolExhausted   = errors.New("subscription account pool exhausted")
	ErrSubscriptionPoolUnavailable = errors.New("subscription account pool unavailable")
	ErrSubscriptionPoolThrottled   = fmt.Errorf("%w: every blocking account is rate-limited", ErrSubscriptionPoolUnavailable)
	ErrCallerPlanThrottled         = fmt.Errorf("%w: caller plan is rate-limited", ErrSubscriptionPoolThrottled)
)

type SubscriptionPoolVerdict struct {
	Refused  subscriptionlane.State
	Sentinel error
}

var SubscriptionPoolVerdicts = map[subscriptionlane.Event]SubscriptionPoolVerdict{
	subscriptionlane.SubscriptionPoolExhausted:   {Refused: subscriptionlane.RefusedSubscriptionPoolExhausted, Sentinel: ErrSubscriptionPoolExhausted},
	subscriptionlane.SubscriptionPoolUnavailable: {Refused: subscriptionlane.RefusedSubscriptionPoolUnavailable, Sentinel: ErrSubscriptionPoolUnavailable},
	subscriptionlane.SubscriptionPoolThrottled:   {Refused: subscriptionlane.RefusedSubscriptionPoolThrottled, Sentinel: ErrSubscriptionPoolThrottled},
	subscriptionlane.CallerPlanThrottled:         {Refused: subscriptionlane.RefusedSubscriptionPoolThrottled, Sentinel: ErrCallerPlanThrottled},
}

func SubscriptionPoolRefusal(ctx context.Context, on subscriptionlane.Event) error {
	verdict := SubscriptionPoolVerdicts[on]
	if next, _ := subscriptionlane.Step(billing.SubscriptionLaneFromContext(ctx), on); next == verdict.Refused {
		return verdict.Sentinel
	}
	return errors.Join(SubscriptionOnlyUnavailable(ctx), verdict.Sentinel)
}

func SubscriptionPoolUnavailableBecause(ctx context.Context, reason string) error {
	return fmt.Errorf("%w: %s", SubscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolUnavailable), reason)
}

func SubscriptionPoolExhausted(ctx context.Context) error {
	return SubscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolExhausted)
}

func MissingQuotaSnapshotRefusal(ctx context.Context, pool subscriptions.Provider, snapshotPresent bool) error {
	if snapshotPresent || !billing.WeaveCreditsDeniedFromContext(ctx) {
		return nil
	}
	enrolled, _ := ctx.Value(ManagedSubscriptionProvidersContextKey{}).(map[auth.SubscriptionProvider]struct{})
	if _, present := enrolled[auth.SubscriptionProvider(pool)]; present {
		return SubscriptionPoolUnavailableBecause(ctx, "enrolled pool has no quota snapshot")
	}
	return nil
}
