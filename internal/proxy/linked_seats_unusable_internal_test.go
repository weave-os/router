package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

func TestPoolExhaustionOwnerFollowsLinkedSeatState(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	spentUntil := now.Add(24 * time.Hour)
	for _, scenario := range []struct {
		name  string
		seats []*auth.SubscriptionAccount
		owner FailureOwner
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
	} {
		t.Run(scenario.name, func(t *testing.T) {
			svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(&scriptedSubscriptionLeaser{})
			svc.clock = func() time.Time { return now }
			ctx := WithManagedSubscriptionCandidates(managedSubscriptionTestContext(), scenario.seats)

			_, _, managed, err := svc.leaseManagedSubscription(ctx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String())

			require.True(t, managed)
			require.ErrorIs(t, err, ErrSubscriptionPoolExhausted)
			assert.Equal(t, scenario.owner, classifyFailureOwner(failureOwnerInput{Err: err}))
		})
	}
}
