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

var subscriptionOnlyReasonForLane = map[subscriptionlane.State]SubscriptionOnlyReason{
	subscriptionlane.LinkedFirst:     SubscriptionOnlyLinkedFirst,
	subscriptionlane.SpendCapReached: SubscriptionOnlySpendCapReached,
	subscriptionlane.CreditsDepleted: SubscriptionOnlyCreditsDepleted,
}

var laneForSubscriptionOnlyReason = map[SubscriptionOnlyReason]subscriptionlane.State{
	SubscriptionOnlyLinkedFirst:     subscriptionlane.LinkedFirst,
	SubscriptionOnlySpendCapReached: subscriptionlane.SpendCapReached,
	SubscriptionOnlyCreditsDepleted: subscriptionlane.CreditsDepleted,
}

var markEventForSubscriptionOnlyReason = map[SubscriptionOnlyReason]subscriptionlane.Event{
	SubscriptionOnlyLinkedFirst:     subscriptionlane.LinkedPlanPreferred,
	SubscriptionOnlySpendCapReached: subscriptionlane.KeySpendCapCovered,
	SubscriptionOnlyCreditsDepleted: subscriptionlane.CreditsDepletedCovered,
}

func SubscriptionLaneFromContext(ctx context.Context) subscriptionlane.State {
	reason, ok := subscriptionOnlyReason(ctx)
	if !ok {
		return subscriptionlane.Funded
	}
	return laneForSubscriptionOnlyReason[reason]
}

func StepSubscriptionLane(ctx context.Context, on subscriptionlane.Event) (context.Context, subscriptionlane.State) {
	from := SubscriptionLaneFromContext(ctx)
	next, _ := subscriptionlane.Step(from, on)
	if next == from || next.Terminal() {
		return ctx, next
	}
	reason, marked := subscriptionOnlyReasonForLane[next]
	if !marked {
		return context.WithValue(ctx, subscriptionOnlyContextKeyT{}, nil), next
	}
	return context.WithValue(ctx, subscriptionOnlyContextKeyT{}, reason), next
}

func WithSubscriptionOnly(ctx context.Context, reason SubscriptionOnlyReason) context.Context {
	on, known := markEventForSubscriptionOnlyReason[reason]
	if !known {
		return ctx
	}
	ctx, _ = StepSubscriptionLane(ctx, on)
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

func subscriptionOnlyReason(ctx context.Context) (reason SubscriptionOnlyReason, ok bool) {
	reason, ok = ctx.Value(subscriptionOnlyContextKeyT{}).(SubscriptionOnlyReason)
	return reason, ok
}
