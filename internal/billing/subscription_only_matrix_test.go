package billing_test

import (
	"context"
	"testing"

	"weave-os/router/internal/billing"

	"github.com/stretchr/testify/assert"
)

type subscriptionOnlyMark struct {
	reason  billing.SubscriptionOnlyReason
	flagged bool
}

var subscriptionOnlyStarts = map[string]subscriptionOnlyMark{
	"unflagged":    {},
	"linked-first": {billing.SubscriptionOnlyLinkedFirst, true},
	"spend-cap":    {billing.SubscriptionOnlySpendCapReached, true},
	"depleted":     {billing.SubscriptionOnlyCreditsDepleted, true},
}

func markedContext(mark subscriptionOnlyMark) context.Context {
	if !mark.flagged {
		return context.Background()
	}
	return billing.WithSubscriptionOnly(context.Background(), mark.reason)
}

func assertMark(t *testing.T, ctx context.Context, want subscriptionOnlyMark) {
	t.Helper()
	reason, ok := billing.SubscriptionOnlyReasonFromContext(ctx)
	assert.Equal(t, want.flagged, ok)
	assert.Equal(t, want.reason, reason)
	assert.Equal(t, want.flagged, billing.SubscriptionOnlyFromContext(ctx))
}

func TestSubscriptionOnlyMarkMatrix(t *testing.T) {
	linkedFirst := subscriptionOnlyMark{billing.SubscriptionOnlyLinkedFirst, true}
	spendCap := subscriptionOnlyMark{billing.SubscriptionOnlySpendCapReached, true}
	depleted := subscriptionOnlyMark{billing.SubscriptionOnlyCreditsDepleted, true}
	want := map[string]map[billing.SubscriptionOnlyReason]subscriptionOnlyMark{
		"unflagged": {
			billing.SubscriptionOnlyLinkedFirst:     linkedFirst,
			billing.SubscriptionOnlySpendCapReached: spendCap,
			billing.SubscriptionOnlyCreditsDepleted: depleted,
		},
		"linked-first": {
			billing.SubscriptionOnlyLinkedFirst:     linkedFirst,
			billing.SubscriptionOnlySpendCapReached: spendCap,
			billing.SubscriptionOnlyCreditsDepleted: depleted,
		},
		"spend-cap": {
			billing.SubscriptionOnlyLinkedFirst:     spendCap,
			billing.SubscriptionOnlySpendCapReached: spendCap,
			billing.SubscriptionOnlyCreditsDepleted: depleted,
		},
		"depleted": {
			billing.SubscriptionOnlyLinkedFirst:     depleted,
			billing.SubscriptionOnlySpendCapReached: depleted,
			billing.SubscriptionOnlyCreditsDepleted: depleted,
		},
	}
	for start, mark := range subscriptionOnlyStarts {
		for reason, expected := range want[start] {
			t.Run(start+"/"+string(reason), func(t *testing.T) {
				assertMark(t, billing.WithSubscriptionOnly(markedContext(mark), reason), expected)
			})
		}
	}
}

func TestReleaseLinkedFirstMatrix(t *testing.T) {
	want := map[string]subscriptionOnlyMark{
		"unflagged":    {},
		"linked-first": {},
		"spend-cap":    {billing.SubscriptionOnlySpendCapReached, true},
		"depleted":     {billing.SubscriptionOnlyCreditsDepleted, true},
	}
	for start, mark := range subscriptionOnlyStarts {
		t.Run(start, func(t *testing.T) {
			assertMark(t, billing.ReleaseLinkedFirst(markedContext(mark)), want[start])
		})
	}
}
