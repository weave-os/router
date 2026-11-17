package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/catalog"
)

func TestPoolExhaustionOwnerFollowsLinkedSeatState(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	spentUntil := now.Add(30 * 24 * time.Hour)
	throttledUntil := now.Add(time.Minute)
	for _, scenario := range []struct {
		name    string
		overage bool
		seats   []*auth.SubscriptionAccount
		owner   FailureOwner
	}{
		{name: "every linked seat disabled, spent or disconnected", seats: []*auth.SubscriptionAccount{
			{ID: "disabled", Provider: auth.SubscriptionProviderClaude, Enabled: false, State: auth.SubscriptionAccountStateDisabled},
			{ID: "disconnected", Provider: auth.SubscriptionProviderClaude, Enabled: false, State: auth.SubscriptionAccountStateReconnectRequired, CredentialRejections: 3},
			{ID: "spent", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &spentUntil},
		}, owner: FailureOwnerClient},
		{name: "a healthy linked seat the pool failed to lease", seats: []*auth.SubscriptionAccount{
			{ID: "healthy", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
			{ID: "disabled", Provider: auth.SubscriptionProviderClaude, Enabled: false, State: auth.SubscriptionAccountStateDisabled},
		}, owner: FailureOwnerRouter},
		{name: "an exhausted seat with no recorded reset", seats: []*auth.SubscriptionAccount{
			{ID: "exhausted", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateExhausted},
		}, owner: FailureOwnerClient},
		{name: "a seat in a short provider rate-limit cooldown", seats: []*auth.SubscriptionAccount{
			{ID: "throttled", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &throttledUntil},
			{ID: "disabled", Provider: auth.SubscriptionProviderClaude, Enabled: false, State: auth.SubscriptionAccountStateDisabled},
		}, owner: FailureOwnerProvider},
		{name: "an exhausted seat that extra usage still lets serve", overage: true, seats: []*auth.SubscriptionAccount{
			{ID: "overage", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &spentUntil},
		}, owner: FailureOwnerRouter},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(&scriptedSubscriptionLeaser{})
			svc.clock = func() time.Time { return now }
			ctx := WithManagedSubscriptionCandidates(managedSubscriptionTestContext(), scenario.seats)
			if scenario.overage {
				ctx = requestcontext.WithSubscriptionExtraUsage(ctx)
			}

			_, _, managed, err := svc.leaseManagedSubscription(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String())

			require.True(t, managed)
			require.ErrorIs(t, err, ErrSubscriptionPoolExhausted)
			assert.Equal(t, scenario.owner, classifyFailureOwner(failureOwnerInput{Err: err}))
		})
	}
}
