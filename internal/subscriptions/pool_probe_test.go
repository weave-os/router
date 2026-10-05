package subscriptions_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

// Once a cooldown has passed, the account takes one request to test whether it
// recovered. Concurrent requests do not pile onto an account that may still
// be rejecting.
func TestPoolLetsOneProbeThroughAfterCooldown(t *testing.T) {
	now := time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)
	p := subscriptions.NewPool("user-a", subscriptions.ProviderClaude, func() time.Time { return now })
	require.NoError(t, p.Upsert(subscriptions.Account{ID: "claude", OwnerID: "user-a", Provider: subscriptions.ProviderClaude, AccessToken: "secret", Enabled: true}))
	require.True(t, p.Cooldown("claude", now.Add(time.Minute)))

	_, _, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "", nil)
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount, "still cooling")

	now = now.Add(2 * time.Minute)
	probe, release, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "", nil)
	require.NoError(t, err)
	require.Equal(t, "claude", probe.ID)

	_, _, err = p.Lease(context.Background(), subscriptions.ProviderClaude, "", nil)
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount, "a probe is already in flight")

	release()
	require.True(t, p.Activate("claude"))
	_, second, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "", nil)
	require.NoError(t, err, "a successful probe reopens the account")
	_, third, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "", nil)
	require.NoError(t, err, "an active account serves concurrent requests")
	second()
	third()
}

func TestPoolKeepsActiveAccountConcurrent(t *testing.T) {
	p := subscriptions.NewPool("user-a", subscriptions.ProviderClaude, nil)
	require.NoError(t, p.Upsert(subscriptions.Account{
		ID: "claude", OwnerID: "user-a", Provider: subscriptions.ProviderClaude, AccessToken: "secret",
		Enabled: true, State: auth.SubscriptionAccountStateActive,
	}))
	_, first, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "", nil)
	require.NoError(t, err)
	_, second, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "", nil)
	require.NoError(t, err)
	first()
	second()
}
