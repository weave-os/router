package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/billing"

	"github.com/stretchr/testify/assert"
)

// The subscription-only warning replaces the routing marker, so it must name
// the gate that actually disabled paid fallback — a reached spend cap is not a
// depleted balance — and stay off the linked-first path every subscriber takes.
func TestSubscriptionOnlyWarningsForReason(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx  context.Context
		want string
	}{
		"not subscription-only": {context.Background(), ""},
		"credits depleted": {
			billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted),
			subscriptionOnlyWarningMarker,
		},
		"spend cap reached": {
			billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlySpendCapReached),
			subscriptionSpendCapWarningMarker,
		},
		"linked first": {
			billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyLinkedFirst),
			"",
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, anthropicSubscriptionOnlyWarnings.forReason(tc.ctx))
		})
	}
}

func TestSpendCapWarningDoesNotClaimDepletedCredits(t *testing.T) {
	for _, warning := range []string{subscriptionSpendCapWarningMarker, subscriptionSpendCapWarningMarkerCodex} {
		assert.NotContains(t, warning, "credits")
		assert.NotContains(t, warning, topUpURL)
		assert.Contains(t, warning, "spend cap")
	}
}

func TestSubscriptionOnlyUnavailableNamesTheGate(t *testing.T) {
	capCtx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlySpendCapReached)
	assert.Equal(t, ErrSpendCapSubscriptionUnavailable, subscriptionOnlyUnavailable(capCtx))
	assert.ErrorIs(t, subscriptionOnlyUnavailable(capCtx), ErrCreditsExhaustedSubscriptionUnavailable,
		"refusal checks match the credits sentinel, so the cap variant must wrap it")

	depletedCtx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted)
	assert.Equal(t, ErrCreditsExhaustedSubscriptionUnavailable, subscriptionOnlyUnavailable(depletedCtx))
}
