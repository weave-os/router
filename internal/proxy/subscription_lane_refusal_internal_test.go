package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/refusal"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type subscriptionLaneRefusalCase struct {
	ctx              context.Context
	unavailable      refusal.Kind
	poolExhausted    refusal.Kind
	paidUnfunded     bool
	linkedFirstStill bool
}

func subscriptionLaneRefusalCases() map[string]subscriptionLaneRefusalCase {
	marked := func(reason billing.SubscriptionOnlyReason) context.Context {
		return billing.WithSubscriptionOnly(context.Background(), reason)
	}
	return map[string]subscriptionLaneRefusalCase{
		"unflagged": {
			ctx:           context.Background(),
			unavailable:   refusal.CreditsExhausted,
			poolExhausted: refusal.SubscriptionPoolExhausted,
		},
		"linked-first": {
			ctx:              marked(billing.SubscriptionOnlyLinkedFirst),
			unavailable:      refusal.CreditsExhausted,
			poolExhausted:    refusal.SubscriptionPoolExhausted,
			linkedFirstStill: true,
		},
		"spend-cap": {
			ctx:           marked(billing.SubscriptionOnlySpendCapReached),
			unavailable:   refusal.SpendCapReached,
			poolExhausted: refusal.SpendCapReached,
			paidUnfunded:  true,
		},
		"depleted": {
			ctx:           marked(billing.SubscriptionOnlyCreditsDepleted),
			unavailable:   refusal.CreditsExhausted,
			poolExhausted: refusal.CreditsExhausted,
			paidUnfunded:  true,
		},
	}
}

func classifiedRefusal(t *testing.T, err error) refusal.Contract {
	t.Helper()
	cls, ok := ClassifyDispatchError(err)
	require.True(t, ok)
	require.NotNil(t, cls.Refusal)
	return *cls.Refusal
}

func TestSubscriptionLaneRefusalsPerMark(t *testing.T) {
	for name, tc := range subscriptionLaneRefusalCases() {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.unavailable.Contract(), classifiedRefusal(t, subscriptionOnlyUnavailable(tc.ctx)))
			assert.Equal(t, tc.poolExhausted.Contract(), classifiedRefusal(t, subscriptionPoolExhausted(tc.ctx)))
			assert.ErrorIs(t, subscriptionPoolExhausted(tc.ctx), ErrSubscriptionPoolExhausted)
			assert.Equal(t, tc.paidUnfunded, paidCapacityUnfunded(tc.ctx))
			assert.Equal(t, tc.linkedFirstStill, linkedFirst(tc.ctx))
		})
	}
}
