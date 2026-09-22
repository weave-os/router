package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/billing"

	"github.com/stretchr/testify/assert"
)

// The depleted-credits warning replaces the routing marker and ignores the
// marker opt-out, so it must fire only when the organization genuinely could
// not fund the turn — not on the linked-first path every subscriber takes.
func TestSubscriptionOnlyWarnsDepleted(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx  context.Context
		want bool
	}{
		"not subscription-only": {context.Background(), false},
		"credits depleted": {
			billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted),
			true,
		},
		"linked first": {
			billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyLinkedFirst),
			false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, subscriptionOnlyWarnsDepleted(tc.ctx))
		})
	}
}
