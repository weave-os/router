package postgres_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/subscriptions"
)

type resetAccountHealthRepository interface {
	UpdateSubscriptionAccountHealth(context.Context, string, auth.SubscriptionOwner, auth.SubscriptionAccountState, bool, *time.Time) error
}

func resetAccountFixture(t *testing.T, fixture subscriptionFixture) (auth.SubscriptionOwner, *auth.SubscriptionAccount) {
	t.Helper()
	owner := auth.SubscriptionOwner{InstallationID: fixture.installationID.String(), SubscriberID: fixture.subscriberA.String(), APIKeyID: fixture.keyA1.String()}
	repo := postgres.NewSubscriptionAccountRepo(fixture.pool)
	account, _, err := repo.UpsertSubscriptionAccount(context.Background(), auth.CreateSubscriptionAccountParams{
		Owner: owner, Provider: auth.SubscriptionProviderCodex, ProviderUserID: "synthetic-reset-user", ExternalAccountID: uuid.NewString(), RefreshToken: []byte("synthetic-encrypted-token"),
	})
	require.NoError(t, err)
	resetAt := time.Now().Add(time.Hour)
	require.NoError(t, repo.(resetAccountHealthRepository).UpdateSubscriptionAccountHealth(context.Background(), account.ID, owner, auth.SubscriptionAccountStateExhausted, true, &resetAt))
	return owner, account
}

func TestCodexResetStoreDurableSelectionAndLeaseFencing(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	owner, account := resetAccountFixture(t, fixture)
	store := postgres.NewCodexResetStore(fixture.pool)
	peer := postgres.NewCodexResetStore(fixture.pool)
	ctx := context.Background()
	claim, acquired, err := store.AcquireCodexReset(ctx, owner.SubscriberID, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	_, acquired, err = peer.AcquireCodexReset(ctx, owner.SubscriberID, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.False(t, acquired)
	claim.AccountID, claim.CreditID, claim.RequestID = account.ID, "specific-credit", uuid.NewString()
	require.NoError(t, store.SelectCodexReset(ctx, claim))
	require.NoError(t, store.ReleaseCodexReset(ctx, claim))
	resumed, acquired, err := peer.AcquireCodexReset(ctx, owner.SubscriberID, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, claim.AccountID, resumed.AccountID)
	require.Equal(t, claim.CreditID, resumed.CreditID)
	require.Equal(t, claim.RequestID, resumed.RequestID)
	require.ErrorIs(t, store.RecoverCodexAccount(ctx, claim, account.ID), subscriptions.ErrResetClaimLost)
	require.ErrorIs(t, store.ClearCodexReset(ctx, claim), subscriptions.ErrResetClaimLost)
	require.ErrorIs(t, store.ReleaseCodexReset(ctx, claim), subscriptions.ErrResetClaimLost)
	repo := postgres.NewSubscriptionAccountRepo(fixture.pool)
	before, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, owner)
	require.NoError(t, err)
	require.Equal(t, auth.SubscriptionAccountStateExhausted, before.State)
	require.NotNil(t, before.CooldownUntil)
	require.NoError(t, peer.RecoverCodexAccount(ctx, resumed, account.ID))
	after, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, owner)
	require.NoError(t, err)
	require.Equal(t, auth.SubscriptionAccountStateActive, after.State)
	require.Nil(t, after.CooldownUntil)
	require.NoError(t, peer.ReleaseCodexReset(ctx, resumed))
	cleared, acquired, err := store.AcquireCodexReset(ctx, owner.SubscriberID, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Empty(t, cleared.CreditID)
}

func TestCodexResetStoreRejectsForeignAndDisabledAccounts(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	owner, account := resetAccountFixture(t, fixture)
	store := postgres.NewCodexResetStore(fixture.pool)
	ctx := context.Background()
	foreign, acquired, err := store.AcquireCodexReset(ctx, fixture.subscriberB.String(), uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	foreign.AccountID, foreign.CreditID, foreign.RequestID = account.ID, "foreign-credit", uuid.NewString()
	require.ErrorIs(t, store.SelectCodexReset(ctx, foreign), subscriptions.ErrResetClaimLost)
	require.ErrorIs(t, store.RecoverCodexAccount(ctx, foreign, account.ID), subscriptions.ErrResetClaimLost)
	claim, acquired, err := store.AcquireCodexReset(ctx, owner.SubscriberID, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	claim.AccountID, claim.CreditID, claim.RequestID = account.ID, "own-credit", uuid.NewString()
	require.NoError(t, store.SelectCodexReset(ctx, claim))
	repo := postgres.NewSubscriptionAccountRepo(fixture.pool)
	require.NoError(t, repo.(resetAccountHealthRepository).UpdateSubscriptionAccountHealth(ctx, account.ID, owner, auth.SubscriptionAccountStateDisabled, false, nil))
	require.ErrorIs(t, store.RecoverCodexAccount(ctx, claim, account.ID), subscriptions.ErrResetClaimLost)
	after, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, owner)
	require.NoError(t, err)
	require.False(t, after.Enabled)
	require.Equal(t, auth.SubscriptionAccountStateDisabled, after.State)
}

func TestCodexResetStoreConcurrentAcquisitionAndCrashTakeover(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	store := postgres.NewCodexResetStore(fixture.pool)
	ctx := context.Background()
	var acquiredCount atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, acquired, err := store.AcquireCodexReset(ctx, fixture.subscriberA.String(), uuid.NewString(), time.Minute)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			if acquired {
				acquiredCount.Add(1)
			}
		}()
	}
	workers.Wait()
	require.Equal(t, int32(1), acquiredCount.Load())
	// A different subscriber proceeds independently. Its expired lease can be
	// reclaimed, while the old holder cannot publish headroom or clear selection.
	expired, acquired, err := store.AcquireCodexReset(ctx, fixture.subscriberB.String(), uuid.NewString(), -time.Second)
	require.NoError(t, err)
	require.True(t, acquired)
	replacement, acquired, err := store.AcquireCodexReset(ctx, fixture.subscriberB.String(), uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotEqual(t, expired.LeaseID, replacement.LeaseID)
	require.ErrorIs(t, store.ClearCodexReset(ctx, expired), subscriptions.ErrResetClaimLost)
}

func TestCodexResetCanRefreshTokensWhileQuotaRemainsExhausted(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	owner, account := resetAccountFixture(t, fixture)
	repo := postgres.NewSubscriptionAccountRepo(fixture.pool)
	ctx := context.Background()
	leaseID := uuid.NewString()
	acquired, err := repo.TryAcquireSubscriptionRefreshLease(ctx, account.ID, owner, leaseID, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired.Acquired)
	before, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, owner)
	require.NoError(t, err)
	require.NoError(t, repo.PersistSubscriptionTokens(ctx, account.ID, owner, leaseID, before.TokenRefreshVersion, []byte("rotated"), []byte("access"), time.Now().Add(time.Hour)))
	after, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, owner)
	require.NoError(t, err)
	require.Equal(t, auth.SubscriptionAccountStateExhausted, after.State)
	require.Equal(t, before.CooldownUntil, after.CooldownUntil)
}
