package billing_test

import (
	"context"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/subscriptionlane"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func contextAtLane(t *testing.T, state subscriptionlane.State) context.Context {
	t.Helper()
	reasons := map[subscriptionlane.State]billing.SubscriptionOnlyReason{
		subscriptionlane.LinkedFirst:     billing.SubscriptionOnlyLinkedFirst,
		subscriptionlane.SpendCapReached: billing.SubscriptionOnlySpendCapReached,
		subscriptionlane.CreditsDepleted: billing.SubscriptionOnlyCreditsDepleted,
	}
	ctx := context.Background()
	if reason, ok := reasons[state]; ok {
		ctx = billing.WithSubscriptionOnly(ctx, reason)
	}
	require.Equal(t, state, billing.SubscriptionLaneFromContext(ctx))
	return ctx
}

func TestStepSubscriptionLaneFollowsTheTransitionTable(t *testing.T) {
	for _, from := range subscriptionlane.States() {
		if from.Terminal() {
			continue
		}
		for _, on := range subscriptionlane.Events() {
			t.Run(from.String()+"/"+on.String(), func(t *testing.T) {
				ctx := contextAtLane(t, from)
				want, _ := subscriptionlane.Step(from, on)

				next, got := billing.StepSubscriptionLane(ctx, on)

				assert.Equal(t, want, got)
				if want.Terminal() {
					assert.Equal(t, from, billing.SubscriptionLaneFromContext(next))
					return
				}
				assert.Equal(t, want, billing.SubscriptionLaneFromContext(next))
				assert.Equal(t, want != subscriptionlane.Funded, billing.SubscriptionOnlyFromContext(next))
			})
		}
	}
}
