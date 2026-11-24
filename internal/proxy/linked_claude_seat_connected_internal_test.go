package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
)

func TestLinkedClaudeSeatConnectedIgnoresSeatsThatCannotLease(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		seats []*auth.SubscriptionAccount
		want  bool
	}{
		{name: "no linked seat", want: false},
		{name: "only a codex seat", seats: []*auth.SubscriptionAccount{
			{ID: "codex", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
		}, want: false},
		{name: "disabled and disconnected claude seats", seats: []*auth.SubscriptionAccount{
			{ID: "disabled", Provider: auth.SubscriptionProviderClaude, Enabled: false, State: auth.SubscriptionAccountStateDisabled},
			{ID: "disconnected", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateReconnectRequired},
		}, want: false},
		{name: "an active claude seat", seats: []*auth.SubscriptionAccount{
			{ID: "disabled", Provider: auth.SubscriptionProviderClaude, Enabled: false, State: auth.SubscriptionAccountStateDisabled},
			{ID: "active", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
		}, want: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := credentials.WithManagedSubscriptionCandidates(context.Background(), scenario.seats)
			assert.Equal(t, scenario.want, credentials.LinkedClaudeSeatConnected(ctx))
		})
	}
}
