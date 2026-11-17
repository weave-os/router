package subscriptionlane_test

import (
	"testing"

	"weave-os/router/internal/refusal"
	"weave-os/router/internal/subscriptionlane"

	"github.com/stretchr/testify/assert"
)

func TestPoolUnavailabilityRefusesFromEveryPendingStateRegardlessOfTheMark(t *testing.T) {
	refusals := map[subscriptionlane.Event]refusal.Kind{
		subscriptionlane.SubscriptionPoolUnavailable: refusal.SubscriptionPoolUnavailable,
		subscriptionlane.SubscriptionPoolThrottled:   refusal.SubscriptionPoolThrottled,
		subscriptionlane.CallerPlanThrottled:         refusal.SubscriptionPoolThrottled,
	}
	for on, want := range refusals {
		for _, from := range subscriptionlane.States() {
			if from.Terminal() {
				continue
			}
			next, ok := subscriptionlane.Step(from, on)
			assert.True(t, ok, "%s on %s", from, on)
			kind, refused := next.Refusal()
			assert.True(t, refused, "%s on %s", from, on)
			assert.Equal(t, want, kind, "%s on %s", from, on)
		}
	}
}

func TestPoolExhaustionYieldsToTheOrgGateThatForcedTheLane(t *testing.T) {
	want := map[subscriptionlane.State]subscriptionlane.State{
		subscriptionlane.Funded:          subscriptionlane.RefusedSubscriptionPoolExhausted,
		subscriptionlane.LinkedFirst:     subscriptionlane.RefusedSubscriptionPoolExhausted,
		subscriptionlane.SpendCapReached: subscriptionlane.RefusedSpendCapReached,
		subscriptionlane.CreditsDepleted: subscriptionlane.RefusedCreditsExhausted,
	}
	for from, to := range want {
		next, ok := subscriptionlane.Step(from, subscriptionlane.SubscriptionPoolExhausted)
		assert.True(t, ok, "%s", from)
		assert.Equal(t, to, next, "%s", from)
	}
}

func TestSubscriptionServedSucceedsFromEveryPendingState(t *testing.T) {
	for _, from := range subscriptionlane.States() {
		if from.Terminal() {
			continue
		}
		next, ok := subscriptionlane.Step(from, subscriptionlane.SubscriptionServed)
		assert.True(t, ok, "%s", from)
		assert.Equal(t, subscriptionlane.ServedOnSubscription, next, "%s", from)
	}
}
