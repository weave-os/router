package subscriptions_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

func TestCodexResetMakesVerifiedRecoveryVisibleToOtherReplica(t *testing.T) {
	runtime, accounts, _, resetStore, ctx := resetFixture(t)
	peer := subscriptions.NewRuntime(accounts, nil, nil)
	accounts.accounts[1].State, accounts.accounts[1].CooldownUntil = auth.SubscriptionAccountStateActive, nil
	for _, replica := range []*subscriptions.Runtime{runtime, peer} {
		lease, present, err := replica.Lease(ctx, testOwner, subscriptions.ProviderCodex, "session")
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, "b", lease.AccountID)
		lease.Release()
	}
	for _, replica := range []*subscriptions.Runtime{runtime, peer} {
		require.NoError(t, replica.Exhaust(ctx, testOwner, subscriptions.ProviderCodex, "b", time.Now().Add(time.Hour)))
	}
	accounts.accessTokens["b"] = []byte("post-reset-token")
	_, recovered, err := runtime.ResetCodex(ctx, testOwner, "session")
	require.NoError(t, err)
	require.True(t, recovered)
	require.Empty(t, resetStore.claim.CreditID)
	lease, present, err := peer.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "session")
	require.NoError(t, err, "a fresh durable recovery must supersede the peer's old local cooldown even with resets disabled")
	require.True(t, present)
	require.Equal(t, "b", lease.AccountID)
	require.Equal(t, "post-reset-token", lease.AccessToken, "newer recovery generations also reload credentials refreshed outside this pool")
	lease.Release()
}

func TestCodexResetFencesLateExhaustionFromOldLease(t *testing.T) {
	runtime, accounts, _, _, ctx := resetFixture(t)
	accounts.accounts[1].State, accounts.accounts[1].CooldownUntil = auth.SubscriptionAccountStateActive, nil
	old, _, err := runtime.Lease(ctx, testOwner, subscriptions.ProviderCodex, "session")
	require.NoError(t, err)
	old.Release()
	require.NoError(t, runtime.ExhaustLease(ctx, testOwner, subscriptions.ProviderCodex, old, time.Now().Add(time.Hour)))
	recovered, present, err := runtime.ResetCodex(ctx, testOwner, "session")
	require.NoError(t, err)
	require.True(t, present)
	require.Greater(t, recovered.QuotaResetGeneration, old.QuotaResetGeneration)
	require.NoError(t, runtime.ExhaustLease(ctx, testOwner, subscriptions.ProviderCodex, old, time.Now().Add(time.Hour)))
	next, present, err := runtime.Lease(ctx, testOwner, subscriptions.ProviderCodex, "session")
	require.NoError(t, err, "a pre-recovery failure must not cool down the restored account")
	require.True(t, present)
	require.Equal(t, recovered.QuotaResetGeneration, next.QuotaResetGeneration)
	next.Release()
	require.NoError(t, runtime.ExhaustLease(ctx, testOwner, subscriptions.ProviderCodex, next, time.Now().Add(time.Hour)))
	_, _, err = runtime.Lease(ctx, testOwner, subscriptions.ProviderCodex, "session")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount, "a genuine post-recovery quota failure still exhausts the account")
}
