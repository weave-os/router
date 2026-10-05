package subscriptions

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPoolRefreshContinuesAfterInitiatingCallerCancels(t *testing.T) {
	now := time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)
	pool := NewPool("subscription-owner", ProviderClaude, func() time.Time { return now })
	require.NoError(t, pool.Upsert(Account{
		ID:                   "subscription-account",
		OwnerID:              "subscription-owner",
		Provider:             ProviderClaude,
		Enabled:              true,
		AccessToken:          "expired-token",
		AccessTokenExpiresAt: now.Add(-time.Minute),
	}))

	refreshStarted := make(chan struct{})
	allowRefresh := make(chan struct{})
	var refreshCount atomic.Int32
	refresh := func(ctx context.Context, account Account) (Account, error) {
		if refreshCount.Add(1) == 1 {
			close(refreshStarted)
		}
		<-allowRefresh
		if err := ctx.Err(); err != nil {
			return Account{}, err
		}
		account.AccessToken = "fresh-token"
		account.AccessTokenExpiresAt = now.Add(time.Hour)
		return account, nil
	}

	type leaseResult struct {
		account Account
		release func()
		err     error
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstResult := make(chan leaseResult, 1)
	go func() {
		account, release, err := pool.Lease(firstCtx, ProviderClaude, "", refresh)
		firstResult <- leaseResult{account: account, release: release, err: err}
	}()
	<-refreshStarted

	secondResult := make(chan leaseResult, 1)
	go func() {
		account, release, err := pool.Lease(context.Background(), ProviderClaude, "", refresh)
		secondResult <- leaseResult{account: account, release: release, err: err}
	}()
	require.Eventually(t, func() bool {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		state := pool.accounts["subscription-account"]
		return state != nil && state.leased == 2
	}, time.Second, time.Millisecond, "the second borrower should join the in-flight refresh")

	cancelFirst()
	close(allowRefresh)

	first := <-firstResult
	require.ErrorIs(t, first.err, context.Canceled)
	if first.release != nil {
		first.release()
	}
	second := <-secondResult
	require.NoError(t, second.err)
	require.Equal(t, "fresh-token", second.account.AccessToken)
	require.Equal(t, int32(1), refreshCount.Load())
	second.release()
}
