package subscriptionlane_test

import (
	"testing"

	"weave-os/router/internal/refusal"
	"weave-os/router/internal/subscriptionlane"

	"github.com/stretchr/testify/assert"
)

func TestTrippedGateRefusesUncoveredAndMarksCovered(t *testing.T) {
	want := map[subscriptionlane.Gate]struct {
		covered subscriptionlane.State
		refusal refusal.Kind
	}{
		subscriptionlane.BalanceGate:            {subscriptionlane.CreditsDepleted, refusal.InsufficientCredits},
		subscriptionlane.KeySpendCapGate:        {subscriptionlane.SpendCapReached, refusal.KeySpendCapReached},
		subscriptionlane.OrgMonthlySpendCapGate: {subscriptionlane.SpendCapReached, refusal.OrgMonthlySpendLimitReached},
		subscriptionlane.DelegatedSpendCapGate:  {subscriptionlane.SpendCapReached, refusal.DelegatedSpendCapReached},
		subscriptionlane.UserSpendLimitGate:     {subscriptionlane.SpendCapReached, refusal.UserSpendLimitReached},
	}
	assert.Len(t, want, len(subscriptionlane.Gates()))
	for _, gate := range subscriptionlane.Gates() {
		covered, ok := subscriptionlane.Step(subscriptionlane.Funded, gate.Tripped(true))
		assert.True(t, ok)
		assert.Equal(t, want[gate].covered, covered)

		for _, from := range subscriptionlane.States() {
			if from.Terminal() {
				continue
			}
			refused, ok := subscriptionlane.Step(from, gate.Tripped(false))
			assert.True(t, ok)
			kind, isRefusal := refused.Refusal()
			assert.True(t, isRefusal, "%s on %s", from, gate.Tripped(false))
			assert.Equal(t, want[gate].refusal, kind)
		}
	}
}

func TestEveryGateEventIsDistinct(t *testing.T) {
	seen := map[subscriptionlane.Event]bool{}
	for _, gate := range subscriptionlane.Gates() {
		for _, covers := range []bool{true, false} {
			on := gate.Tripped(covers)
			assert.False(t, seen[on], on.String())
			seen[on] = true
		}
	}
}
