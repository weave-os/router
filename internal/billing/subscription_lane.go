package billing

import (
	"context"

	"weave-os/router/internal/subscriptionlane"
)

type subscriptionOnlyContextKeyT struct{}

type SubscriptionOnlyReason string

const (
	SubscriptionOnlyCreditsDepleted SubscriptionOnlyReason = "credits_depleted"
	SubscriptionOnlySpendCapReached SubscriptionOnlyReason = "spend_cap_reached"
	SubscriptionOnlyLinkedFirst     SubscriptionOnlyReason = "linked_first"
)

type subscriptionOnlyRow struct {
	reason SubscriptionOnlyReason
	lane   subscriptionlane.State
	mark   subscriptionlane.Event
}

var subscriptionOnlyRows = []subscriptionOnlyRow{
	{SubscriptionOnlyLinkedFirst, subscriptionlane.LinkedFirst, subscriptionlane.LinkedPlanPreferred},
	{SubscriptionOnlySpendCapReached, subscriptionlane.SpendCapReached, subscriptionlane.KeySpendCapCovered},
	{SubscriptionOnlyCreditsDepleted, subscriptionlane.CreditsDepleted, subscriptionlane.CreditsDepletedCovered},
}

var unknownSubscriptionOnlyRow = subscriptionOnlyRows[len(subscriptionOnlyRows)-1]

func subscriptionOnlyRowForReason(reason SubscriptionOnlyReason) subscriptionOnlyRow {
	for _, row := range subscriptionOnlyRows {
		if row.reason == reason {
			return row
		}
	}
	return unknownSubscriptionOnlyRow
}

func subscriptionOnlyReasonForLane(lane subscriptionlane.State) (SubscriptionOnlyReason, bool) {
	for _, row := range subscriptionOnlyRows {
		if row.lane == lane {
			return row.reason, true
		}
	}
	return "", false
}

func SubscriptionLaneFromContext(ctx context.Context) subscriptionlane.State {
	reason, ok := subscriptionOnlyReason(ctx)
	if !ok {
		return subscriptionlane.Funded
	}
	return subscriptionOnlyRowForReason(reason).lane
}

func StepSubscriptionLane(ctx context.Context, on subscriptionlane.Event) (context.Context, subscriptionlane.State) {
	from := SubscriptionLaneFromContext(ctx)
	next, _ := subscriptionlane.Step(from, on)
	if next == from || next.Terminal() {
		return ctx, next
	}
	reason, marked := subscriptionOnlyReasonForLane(next)
	if !marked {
		return context.WithValue(ctx, subscriptionOnlyContextKeyT{}, nil), next
	}
	return context.WithValue(ctx, subscriptionOnlyContextKeyT{}, reason), next
}

func WithSubscriptionOnly(ctx context.Context, reason SubscriptionOnlyReason) context.Context {
	ctx, _ = StepSubscriptionLane(ctx, subscriptionOnlyRowForReason(reason).mark)
	return ctx
}

func ReleaseLinkedFirst(ctx context.Context) context.Context {
	ctx, _ = StepSubscriptionLane(ctx, subscriptionlane.LinkedPlanReleased)
	return ctx
}

func SubscriptionOnlyFromContext(ctx context.Context) bool {
	return SubscriptionLaneFromContext(ctx) != subscriptionlane.Funded
}

func SubscriptionOnlyReasonFromContext(ctx context.Context) (reason SubscriptionOnlyReason, ok bool) {
	return subscriptionOnlyReason(ctx)
}

func unfundedTurn(ctx context.Context) bool {
	return !subscriptionlane.Permits(SubscriptionLaneFromContext(ctx), subscriptionlane.OrganizationCreditsServed)
}

type byokFeeUnpayableKey struct{}

func (s *Service) TripSubscriptionLane(ctx context.Context, on subscriptionlane.Event) (context.Context, subscriptionlane.State) {
	ctx, lane := StepSubscriptionLane(ctx, on)
	if s.ByokFeeOwed(ctx) && unfundedTurn(ctx) {
		ctx = context.WithValue(ctx, byokFeeUnpayableKey{}, true)
	}
	return ctx, lane
}

func (s *Service) ByokFeeOwed(ctx context.Context) bool {
	return s.byokFeeRate > 0 && !HasOverrideFromContext(ctx)
}

func ByokFeeUnpayable(ctx context.Context) bool {
	unpayable, _ := ctx.Value(byokFeeUnpayableKey{}).(bool)
	return unpayable
}

func subscriptionOnlyReason(ctx context.Context) (reason SubscriptionOnlyReason, ok bool) {
	reason, ok = ctx.Value(subscriptionOnlyContextKeyT{}).(SubscriptionOnlyReason)
	return reason, ok
}
