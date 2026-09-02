package subscriptions_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"workweave/router/internal/subscriptions"
)

func TestPoolSeparatesProvidersAndOwners(t *testing.T) {
	p := subscriptions.NewPool("user-a", subscriptions.ProviderClaude, nil)
	require.NoError(t, p.Upsert(subscriptions.Account{ID: "claude", OwnerID: "user-a", Provider: subscriptions.ProviderClaude, AccessToken: "secret", Enabled: true}))
	require.ErrorIs(t, p.Upsert(subscriptions.Account{ID: "codex", OwnerID: "user-a", Provider: subscriptions.ProviderCodex, AccessToken: "secret", Enabled: true}), subscriptions.ErrProviderMismatch)
	require.ErrorIs(t, p.Upsert(subscriptions.Account{ID: "other", OwnerID: "user-b", Provider: subscriptions.ProviderClaude, AccessToken: "secret", Enabled: true}), subscriptions.ErrProviderMismatch)

	acct, release, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "session-a", nil)
	require.NoError(t, err)
	require.Equal(t, "user-a", acct.OwnerID)
	require.Equal(t, "claude", acct.ID)
	release()
}

func TestPoolRefreshesOnceForConcurrentLeases(t *testing.T) {
	p := subscriptions.NewPool("user-a", subscriptions.ProviderClaude, nil)
	require.NoError(t, p.Upsert(subscriptions.Account{ID: "claude", OwnerID: "user-a", Provider: subscriptions.ProviderClaude, Enabled: true}))
	var refreshes atomic.Int32
	refresh := func(_ context.Context, account subscriptions.Account) (subscriptions.Account, error) {
		refreshes.Add(1)
		time.Sleep(5 * time.Millisecond)
		account.AccessToken = "fresh"
		return account, nil
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acct, release, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "", refresh)
			if err == nil {
				require.Equal(t, "fresh", acct.AccessToken)
				release()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), refreshes.Load())
}

func TestPoolUpsertPreservesInflightLeaseCount(t *testing.T) {
	p := subscriptions.NewPool("user-a", subscriptions.ProviderClaude, nil)
	account := subscriptions.Account{ID: "a", OwnerID: "user-a", Provider: subscriptions.ProviderClaude, AccessToken: "old", Enabled: true}
	require.NoError(t, p.Upsert(account))
	require.NoError(t, p.Upsert(subscriptions.Account{ID: "b", OwnerID: "user-a", Provider: subscriptions.ProviderClaude, AccessToken: "backup", Enabled: true}))

	_, releaseFirst, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "", nil)
	require.NoError(t, err)
	account.AccessToken = "new"
	require.NoError(t, p.Upsert(account))

	second, releaseSecond, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "", nil)
	require.NoError(t, err)
	require.Equal(t, "b", second.ID)
	releaseFirst()
	releaseSecond()
}

func TestPoolCanceledRefreshWaiterDoesNotCooldownAccount(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	p := subscriptions.NewPool("user-a", subscriptions.ProviderClaude, func() time.Time { return now })
	require.NoError(t, p.Upsert(subscriptions.Account{ID: "claude", OwnerID: "user-a", Provider: subscriptions.ProviderClaude, Enabled: true}))
	refreshStarted := make(chan struct{})
	allowRefresh := make(chan struct{})
	refresh := func(_ context.Context, account subscriptions.Account) (subscriptions.Account, error) {
		close(refreshStarted)
		<-allowRefresh
		account.AccessToken = "fresh"
		return account, nil
	}

	firstDone := make(chan error, 1)
	go func() {
		_, release, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "", refresh)
		if release != nil {
			release()
		}
		firstDone <- err
	}()
	<-refreshStarted

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	cancelWaiter()
	_, _, err := p.Lease(waiterCtx, subscriptions.ProviderClaude, "", refresh)
	require.ErrorIs(t, err, context.Canceled)
	close(allowRefresh)
	require.NoError(t, <-firstDone)

	account, release, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "", nil)
	require.NoError(t, err)
	require.Equal(t, "fresh", account.AccessToken)
	release()
}

func TestPoolDisableAndRemove(t *testing.T) {
	p := subscriptions.NewPool("user-a", subscriptions.ProviderClaude, nil)
	require.NoError(t, p.Upsert(subscriptions.Account{ID: "claude", OwnerID: "user-a", Provider: subscriptions.ProviderClaude, Enabled: true, AccessToken: "secret"}))
	require.True(t, p.Disable("claude"))
	require.ErrorIs(t, mustLeaseError(p, subscriptions.ProviderClaude), subscriptions.ErrNoAvailableAccount)
	require.True(t, p.Remove("claude"))
	require.False(t, p.Remove("claude"))
}

func TestPoolCooldownRotatesStickyAccount(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	p := subscriptions.NewPool("user-a", subscriptions.ProviderClaude, func() time.Time { return now })
	require.NoError(t, p.Upsert(subscriptions.Account{ID: "a", OwnerID: "user-a", Provider: subscriptions.ProviderClaude, Enabled: true, AccessToken: "a"}))
	require.NoError(t, p.Upsert(subscriptions.Account{ID: "b", OwnerID: "user-a", Provider: subscriptions.ProviderClaude, Enabled: true, AccessToken: "b"}))

	first, release, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "session", nil)
	require.NoError(t, err)
	release()
	require.Equal(t, "a", first.ID)
	require.True(t, p.Cooldown("a", now.Add(time.Minute)))

	rotated, release, err := p.Lease(context.Background(), subscriptions.ProviderClaude, "session", nil)
	require.NoError(t, err)
	release()
	require.Equal(t, "b", rotated.ID)
}

func TestManagerDoesNotCrossOwnerOrProviderPools(t *testing.T) {
	m := subscriptions.NewManager(nil)
	require.NoError(t, m.Upsert(subscriptions.Account{ID: "claude-a", OwnerID: "user-a", Provider: subscriptions.ProviderClaude, Enabled: true, AccessToken: "a"}))
	require.NoError(t, m.Upsert(subscriptions.Account{ID: "codex-a", OwnerID: "user-a", Provider: subscriptions.ProviderCodex, Enabled: true, AccessToken: "c"}))

	_, release, err := m.Lease(context.Background(), "user-b", subscriptions.ProviderClaude, "", nil)
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	require.Nil(t, release)
	_, release, err = m.Lease(context.Background(), "user-a", subscriptions.ProviderCodex, "", nil)
	require.NoError(t, err)
	require.NotNil(t, release)
	release()
}

func mustLeaseError(p *subscriptions.Pool, provider subscriptions.Provider) error {
	_, _, err := p.Lease(context.Background(), provider, "", nil)
	return err
}
