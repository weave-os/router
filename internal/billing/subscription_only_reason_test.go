package billing_test

import (
	"context"
	"testing"

	"weave-os/router/internal/billing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithSubscriptionOnlyReasonPrecedence(t *testing.T) {
	// Only the depleted reason drives the top-up CTA, so a later linked-first
	// mark must not be able to quietly retire it. The reverse escalation has to
	// stay open: that is the direction the mounted gates actually run in.
	for name, tc := range map[string]struct {
		apply func(context.Context) context.Context
		want  billing.SubscriptionOnlyReason
	}{
		"linked-first alone": {
			func(ctx context.Context) context.Context {
				return billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyLinkedFirst)
			},
			billing.SubscriptionOnlyLinkedFirst,
		},
		"depleted alone": {
			func(ctx context.Context) context.Context {
				return billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted)
			},
			billing.SubscriptionOnlyCreditsDepleted,
		},
		"linked-first then depleted escalates": {
			func(ctx context.Context) context.Context {
				ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyLinkedFirst)
				return billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted)
			},
			billing.SubscriptionOnlyCreditsDepleted,
		},
		"depleted then linked-first holds": {
			func(ctx context.Context) context.Context {
				ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted)
				return billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyLinkedFirst)
			},
			billing.SubscriptionOnlyCreditsDepleted,
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := tc.apply(context.Background())

			reason, ok := billing.SubscriptionOnlyReasonFromContext(ctx)
			require.True(t, ok)
			assert.Equal(t, tc.want, reason)
			assert.True(t, billing.SubscriptionOnlyFromContext(ctx))
		})
	}
}

func TestSubscriptionOnlyAbsentByDefault(t *testing.T) {
	reason, ok := billing.SubscriptionOnlyReasonFromContext(context.Background())

	assert.False(t, ok)
	assert.Empty(t, reason)
	assert.False(t, billing.SubscriptionOnlyFromContext(context.Background()))
}
