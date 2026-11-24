package proxy

import (
	"context"
	"fmt"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/subscriptions/entitlement"

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
			unavailable:   refusal.SubscriptionUnavailableForTurn,
			poolExhausted: refusal.SubscriptionPoolExhausted,
		},
		"linked-first": {
			ctx:              marked(billing.SubscriptionOnlyLinkedFirst),
			unavailable:      refusal.SubscriptionUnavailableForTurn,
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
			assert.Equal(t, tc.unavailable.Contract(), classifiedRefusal(t, credentials.SubscriptionOnlyUnavailable(tc.ctx)))
			assert.Equal(t, tc.poolExhausted.Contract(), classifiedRefusal(t, credentials.SubscriptionPoolExhausted(tc.ctx)))
			require.ErrorIs(t, credentials.SubscriptionPoolExhausted(tc.ctx), credentials.ErrSubscriptionPoolExhausted)
			assert.Equal(t, tc.paidUnfunded, credentials.PaidCapacityUnfunded(tc.ctx))
			assert.Equal(t, tc.linkedFirstStill, credentials.LinkedFirst(tc.ctx))
		})
	}
}

func TestUserSpendLimitRefusalClassifiesAsUserSpendLimitReached(t *testing.T) {
	assert.Equal(t, refusal.UserSpendLimitReached.Contract(), classifiedRefusal(t, fmt.Errorf("%w: spent", billing.ErrUserMonthlySpendLimitReached)))
}

func TestIncludedAllowanceSpentOnAFundedTurnIsNotACreditsRefusal(t *testing.T) {
	ctx := entitlement.WithSpentWindow(context.Background(), entitlement.SpentWindow{})

	err := credentials.SubscriptionOnlyUnavailable(ctx)

	var allowanceSpent *credentials.IncludedAllowanceSpentError
	require.ErrorAs(t, err, &allowanceSpent)
	assert.Equal(t, refusal.SubscriptionUnavailableForTurn.Contract(), classifiedRefusal(t, err))
	cls, _ := ClassifyDispatchError(err)
	assert.NotContains(t, cls.Message, topUpURL)
}
