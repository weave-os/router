package subscriptions

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
)

func TestPoolRecoveryGenerationPreservesStaleReadProtection(t *testing.T) {
	now := time.Now()
	pool := NewPool("owner", ProviderCodex, func() time.Time { return now })
	active := Account{ID: "account", OwnerID: "owner", Provider: ProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive, AccessToken: "fresh-token", AccessTokenExpiresAt: now.Add(time.Hour)}
	require.NoError(t, pool.Upsert(active))
	require.True(t, pool.Exhaust(active.ID, now.Add(time.Hour)))
	require.NoError(t, pool.Upsert(active))
	_, _, err := pool.Lease(context.Background(), ProviderCodex, "", nil)
	require.ErrorIs(t, err, ErrNoAvailableAccount, "an equally versioned snapshot cannot shorten a local cooldown")
	recovered := active
	recovered.QuotaResetGeneration = 1
	require.NoError(t, pool.Upsert(recovered))
	stale := active
	stale.State, stale.CooldownTil = auth.SubscriptionAccountStateExhausted, now.Add(time.Hour)
	require.NoError(t, pool.Upsert(stale))
	lease, release, err := pool.Lease(context.Background(), ProviderCodex, "", nil)
	require.NoError(t, err, "a pre-recovery snapshot cannot resurrect the old cooldown")
	require.Equal(t, int64(1), lease.QuotaResetGeneration)
	release()
	recovered.Enabled = false
	require.NoError(t, pool.Upsert(recovered))
	_, _, err = pool.Lease(context.Background(), ProviderCodex, "", nil)
	require.ErrorIs(t, err, ErrNoAvailableAccount, "the generation fence never overrides account revocation")
}

func TestPoolRecoveryGenerationFencesDelayedRefresh(t *testing.T) {
	now := time.Now()
	pool := NewPool("owner", ProviderCodex, func() time.Time { return now })
	account := Account{ID: "account", OwnerID: "owner", Provider: ProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive}
	require.NoError(t, pool.Upsert(account))
	started, resume := make(chan struct{}), make(chan struct{})
	type result struct {
		account Account
		err     error
	}
	done := make(chan result, 1)
	go func() {
		leased, release, err := pool.Lease(context.Background(), ProviderCodex, "", func(context.Context, Account) (Account, error) {
			close(started)
			<-resume
			old := account
			old.AccessToken, old.AccessTokenExpiresAt = "old-token", now.Add(time.Hour)
			return old, nil
		})
		if release != nil {
			release()
		}
		done <- result{leased, err}
	}()
	<-started
	recovered := account
	recovered.QuotaResetGeneration, recovered.AccessToken = 1, "recovered-token"
	recovered.AccessTokenExpiresAt = now.Add(time.Hour)
	require.NoError(t, pool.Upsert(recovered))
	close(resume)
	got := <-done
	require.NoError(t, got.err)
	require.Equal(t, int64(1), got.account.QuotaResetGeneration)
	require.Equal(t, "recovered-token", got.account.AccessToken)
}
