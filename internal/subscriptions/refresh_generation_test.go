package subscriptions_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

func TestManualRefreshReplacesPeerReplicaCachedCredential(t *testing.T) {
	f := newProactiveRefreshFixture(t, false, false)
	oldAccess := f.accessAt(t, 0)
	peer := subscriptions.NewRuntime(f.store, f.issuer, func() time.Time { return f.started })
	warm, _, err := peer.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "peer-session")
	require.NoError(t, err)
	require.Equal(t, oldAccess, warm.AccessToken)
	warm.Release()
	result, err := f.runtime.RefreshAccount(context.Background(), testOwner, "synthetic-seat")
	require.NoError(t, err)
	require.Equal(t, int64(2), result.TokenRefreshVersion)
	adopted, _, err := peer.Lease(context.Background(), testOwner, subscriptions.ProviderCodex, "peer-session")
	require.NoError(t, err)
	defer adopted.Release()
	require.NotEqual(t, oldAccess, adopted.AccessToken, "a peer must stop serving the old generation before it expires")
	require.Equal(t, string(f.store.accessTokens["synthetic-seat"]), adopted.AccessToken)
	require.Equal(t, 2, f.issuer.refreshes, "adopt the published credential without another provider refresh")
}

func TestEarlyRefreshLostLeaseReloadsReplacementGeneration(t *testing.T) {
	f := newProactiveRefreshFixture(t, false, true)
	f.accessAt(t, 0)
	f.now.Store(f.started.Add(6 * simulatedDay).UnixNano())
	clock := func() time.Time { return time.Unix(0, f.now.Load()).UTC() }
	started := make(chan struct{})
	refresher := runtimeRefreshFunc(func(ctx context.Context, _ subscriptions.Provider, _ string) (subscriptions.RefreshedToken, error) {
		close(started)
		<-ctx.Done()
		return subscriptions.RefreshedToken{}, ctx.Err()
	})
	policy, err := subscriptions.NewEarlyRefreshPolicy([]string{"synthetic-seat"}, 6*simulatedDay)
	require.NoError(t, err)
	runtime := subscriptions.NewRuntime(f.store, refresher, clock).WithEarlyRefreshPolicy(policy)
	subscriptions.SetRefreshLeaseTimingForTest(runtime, 60*time.Millisecond, 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type outcome struct {
		lease subscriptions.Lease
		err   error
	}
	completed := make(chan outcome, 1)
	go func() {
		lease, _, err := runtime.Lease(ctx, testOwner, subscriptions.ProviderCodex, "old-session")
		completed <- outcome{lease: lease, err: err}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	f.store.mu.Lock()
	delete(f.store.leaseIDs, "synthetic-seat")
	delete(f.store.leaseUntil, "synthetic-seat")
	f.store.tokenRefreshVersions["synthetic-seat"] = 2
	f.store.accessTokens["synthetic-seat"] = []byte("synthetic-peer-access")
	f.store.refreshTokens["synthetic-seat"] = []byte("synthetic-peer-refresh")
	f.store.refreshedAt["synthetic-seat"] = clock()
	f.store.accessExpiry["synthetic-seat"] = clock().Add(10 * simulatedDay)
	f.store.mu.Unlock()
	result := <-completed
	require.NoError(t, result.err)
	defer result.lease.Release()
	require.Equal(t, "synthetic-peer-access", result.lease.AccessToken, "lease loss must not enter the stale-access fallback")
	require.Empty(t, f.store.cooldowns)
	require.Empty(t, f.store.enabledUpdates)
}

func TestPoolStaleSnapshotCannotReplaceNewerCachedGeneration(t *testing.T) {
	clock := time.Now
	pool := subscriptions.NewPool("synthetic-owner", subscriptions.ProviderCodex, clock)
	account := subscriptions.Account{ID: "synthetic-seat", OwnerID: "synthetic-owner", Provider: subscriptions.ProviderCodex,
		Enabled: true, State: auth.SubscriptionAccountStateActive, AccessToken: "synthetic-new-access",
		AccessTokenExpiresAt: clock().Add(time.Hour), TokenRefreshVersion: 2}
	require.NoError(t, pool.Upsert(account))
	account.AccessToken, account.TokenRefreshVersion = "synthetic-stale-access", 1
	require.NoError(t, pool.Upsert(account))
	leased, release, err := pool.Lease(context.Background(), subscriptions.ProviderCodex, "", nil)
	require.NoError(t, err)
	defer release()
	require.Equal(t, "synthetic-new-access", leased.AccessToken)
	require.Equal(t, int64(2), leased.TokenRefreshVersion)
}
