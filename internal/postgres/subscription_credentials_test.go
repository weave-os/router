package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
)

func TestSubscriptionRefreshTimestampPublishesWithGeneration(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	repo := postgres.NewSubscriptionAccountRepo(fixture.pool)
	ctx := context.Background()
	owner := fixture.subscriberAKeys()[0]
	params := auth.CreateSubscriptionAccountParams{Owner: owner, Provider: auth.SubscriptionProviderClaude,
		ExternalAccountID: "synthetic-refresh-metadata", RefreshToken: []byte("synthetic-encrypted-refresh")}
	account, _, err := repo.UpsertSubscriptionAccount(ctx, params)
	require.NoError(t, err)
	before, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, owner)
	require.NoError(t, err)
	require.Nil(t, before.TokenRefreshedAt)
	leaseID := uuid.NewString()
	lease, err := repo.TryAcquireSubscriptionRefreshLease(ctx, account.ID, owner, leaseID, time.Minute)
	require.NoError(t, err)
	require.True(t, lease.Acquired)
	start := time.Now()
	require.NoError(t, repo.PersistSubscriptionTokens(ctx, account.ID, owner, leaseID, before.TokenRefreshVersion,
		[]byte("synthetic-rotated-refresh"), []byte("synthetic-new-access"), start.Add(time.Hour)))
	published, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, owner)
	require.NoError(t, err)
	require.NotNil(t, published.TokenRefreshedAt)
	require.WithinDuration(t, start, *published.TokenRefreshedAt, 5*time.Second)
	require.Equal(t, before.TokenRefreshVersion+1, published.TokenRefreshVersion)
	candidates, err := repo.(interface {
		ListSubscriptionCandidates(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error)
	}).ListSubscriptionCandidates(ctx, owner)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, published.TokenRefreshVersion, candidates[0].TokenRefreshVersion)

	require.ErrorIs(t, repo.PersistSubscriptionTokens(ctx, account.ID, owner, leaseID, before.TokenRefreshVersion,
		[]byte("synthetic-stale-refresh"), []byte("synthetic-stale-access"), start.Add(time.Hour)), auth.ErrSubscriptionRefreshConflict)
	afterStale, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, owner)
	require.NoError(t, err)
	require.Equal(t, published.TokenRefreshedAt, afterStale.TokenRefreshedAt)
	_, _, err = repo.UpsertSubscriptionAccount(ctx, params)
	require.NoError(t, err)
	reconnected, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, owner)
	require.NoError(t, err)
	require.Nil(t, reconnected.TokenRefreshedAt, "a replacement login must not inherit the old generation's refresh date")
}

func TestMaintenanceRefreshLeaseDoesNotMakeExhaustedQuotaAvailable(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	repo := postgres.NewSubscriptionAccountRepo(fixture.pool)
	ctx := context.Background()
	owner := fixture.subscriberAKeys()[0]
	account, _, err := repo.UpsertSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{
		Owner: owner, Provider: auth.SubscriptionProviderClaude,
		ExternalAccountID: "synthetic-maintenance-seat", RefreshToken: []byte("synthetic-encrypted-refresh")})
	require.NoError(t, err)
	reset := time.Now().Add(time.Hour)
	health := repo.(interface {
		UpdateSubscriptionAccountHealth(context.Context, string, auth.SubscriptionOwner, auth.SubscriptionAccountState, bool, *time.Time) (bool, error)
	})
	_, err = health.UpdateSubscriptionAccountHealth(ctx, account.ID, owner, auth.SubscriptionAccountStateExhausted, true, &reset)
	require.NoError(t, err)
	ordinary, err := repo.TryAcquireSubscriptionRefreshLease(ctx, account.ID, owner, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.False(t, ordinary.Acquired)
	maintenance := repo.(auth.SubscriptionMaintenanceRefreshRepository)
	leaseID := uuid.NewString()
	lease, err := maintenance.TryAcquireSubscriptionMaintenanceRefreshLease(ctx, account.ID, owner, leaseID, time.Minute)
	require.NoError(t, err)
	require.True(t, lease.Acquired)
	before, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, owner)
	require.NoError(t, err)
	require.NoError(t, repo.PersistSubscriptionTokens(ctx, account.ID, owner, leaseID, before.TokenRefreshVersion,
		[]byte("synthetic-new-refresh"), []byte("synthetic-new-access"), time.Now().Add(time.Hour)))
	after, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, owner)
	require.NoError(t, err)
	require.Equal(t, auth.SubscriptionAccountStateExhausted, after.State)
	require.WithinDuration(t, reset, *after.CooldownUntil, time.Microsecond)
	ordinary, err = repo.TryAcquireSubscriptionRefreshLease(ctx, account.ID, owner, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.False(t, ordinary.Acquired)
}
