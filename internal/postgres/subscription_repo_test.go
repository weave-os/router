package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
)

// These exercise the ownership predicates that unit tests can't: a subscriber
// reaching its linked accounts through any of its keys, isolation between two
// subscribers of one installation, and the legacy api-key-owned rows the
// ownership migration deliberately left behind. Gated on
// ROUTER_TEST_DATABASE_URL like the other database-backed tests here.
type subscriptionFixture struct {
	pool           *pgxpool.Pool
	installationID uuid.UUID
	subscriberA    uuid.UUID
	subscriberB    uuid.UUID
	keyA1          uuid.UUID
	keyA2          uuid.UUID
	keyB1          uuid.UUID
	legacyKey      uuid.UUID
}

func newSubscriptionFixture(t *testing.T) subscriptionFixture {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	fixture := subscriptionFixture{
		pool:           pool,
		installationID: uuid.New(),
		subscriberA:    uuid.New(),
		subscriberB:    uuid.New(),
		keyA1:          uuid.New(),
		keyA2:          uuid.New(),
		keyB1:          uuid.New(),
		legacyKey:      uuid.New(),
	}
	_, err := pool.Exec(ctx,
		"INSERT INTO router.model_router_installations (id, external_id, name) VALUES ($1, $2, $2)",
		fixture.installationID, "subscription-ownership-"+fixture.installationID.String()[:8])
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(),
			"DELETE FROM router.model_router_subscription_accounts WHERE api_key_id IN (SELECT id FROM router.model_router_api_keys WHERE installation_id = $1) OR subscriber_id IN ($2, $3)",
			fixture.installationID, fixture.subscriberA, fixture.subscriberB)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(),
			"DELETE FROM router.model_router_api_keys WHERE installation_id = $1", fixture.installationID)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(),
			"DELETE FROM router.credential_subject_installations WHERE installation_id = $1", fixture.installationID)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(),
			"DELETE FROM router.credential_subjects WHERE id IN ($1, $2)", fixture.subscriberA, fixture.subscriberB)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(),
			"DELETE FROM router.model_router_installations WHERE id = $1", fixture.installationID)
		require.NoError(t, cleanupErr)
	})

	for _, subjectID := range []uuid.UUID{fixture.subscriberA, fixture.subscriberB} {
		_, err = pool.Exec(ctx, "INSERT INTO router.credential_subjects (id, projection_complete) VALUES ($1, TRUE)", subjectID)
		require.NoError(t, err)
		_, err = pool.Exec(ctx,
			"INSERT INTO router.credential_subject_installations (subject_id, installation_id, access_enabled) VALUES ($1, $2, TRUE)",
			subjectID, fixture.installationID)
		require.NoError(t, err)
	}
	fixture.insertKey(t, fixture.keyA1, &fixture.subscriberA)
	fixture.insertKey(t, fixture.keyA2, &fixture.subscriberA)
	fixture.insertKey(t, fixture.keyB1, &fixture.subscriberB)
	fixture.insertKey(t, fixture.legacyKey, nil)
	return fixture
}

func (f subscriptionFixture) insertKey(t *testing.T, keyID uuid.UUID, subjectID *uuid.UUID) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(), `
		INSERT INTO router.model_router_api_keys
			(id, installation_id, external_id, key_prefix, key_hash, key_suffix, credential_subject_id)
		VALUES ($1, $2, $3, $4, $5, 'test', $6)`,
		keyID, f.installationID, keyID.String()[:8], "wv_"+keyID.String()[:8],
		"hash-"+keyID.String(), subjectID)
	require.NoError(t, err)
}

func (f subscriptionFixture) legacyRow(t *testing.T, keyID uuid.UUID, provider, externalAccountID string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), `
		INSERT INTO router.model_router_subscription_accounts
			(api_key_id, provider, external_account_id, refresh_token_ciphertext)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		keyID, provider, externalAccountID, []byte("legacy-ciphertext")).Scan(&id))
	return id
}

func TestSubscriptionAccountsFollowSubscriberAcrossKeys(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	repo := postgres.NewSubscriptionAccountRepo(fixture.pool)
	ctx := context.Background()

	enrollingOwner := auth.SubscriptionOwner{InstallationID: fixture.installationID.String(), SubscriberID: fixture.subscriberA.String(), APIKeyID: fixture.keyA1.String()}
	for _, enrollment := range []struct {
		provider          auth.SubscriptionProvider
		externalAccountID string
	}{
		{auth.SubscriptionProviderClaude, "claude-one"},
		{auth.SubscriptionProviderClaude, "claude-two"},
		{auth.SubscriptionProviderCodex, "codex-one"},
	} {
		account, kind, err := repo.UpsertSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{
			Owner: enrollingOwner, Provider: enrollment.provider,
			ExternalAccountID: enrollment.externalAccountID, ProviderUserID: "synthetic-provider-user", RefreshToken: []byte("ciphertext"),
		})
		require.NoError(t, err)
		assert.Equal(t, auth.SubscriptionUpsertInserted, kind)
		assert.Equal(t, fixture.subscriberA.String(), account.SubscriberID)
		assert.Equal(t, fixture.keyA1.String(), account.EnrolledByAPIKeyID, "enrolling key is kept as attribution")
	}

	// A rotated or second harness key of the same subscriber serves the same pool.
	rotatedOwner := auth.SubscriptionOwner{InstallationID: fixture.installationID.String(), SubscriberID: fixture.subscriberA.String(), APIKeyID: fixture.keyA2.String()}
	accounts, err := repo.ListSubscriptionAccounts(ctx, rotatedOwner)
	require.NoError(t, err)
	require.Len(t, accounts, 3)
	byProvider := map[auth.SubscriptionProvider][]string{}
	for _, account := range accounts {
		byProvider[account.Provider] = append(byProvider[account.Provider], account.ExternalAccountID)
	}
	assert.ElementsMatch(t, []string{"claude-one", "claude-two"}, byProvider[auth.SubscriptionProviderClaude])
	assert.Equal(t, []string{"codex-one"}, byProvider[auth.SubscriptionProviderCodex])

	// A physical account cannot be claimed by another subscriber.
	otherOwner := auth.SubscriptionOwner{InstallationID: fixture.installationID.String(), SubscriberID: fixture.subscriberB.String(), APIKeyID: fixture.keyB1.String()}
	_, _, err = repo.UpsertSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{Owner: otherOwner, Provider: auth.SubscriptionProviderClaude, ExternalAccountID: "claude-one", RefreshToken: []byte("forbidden")})
	require.Error(t, err)
	otherAccount, _, err := repo.UpsertSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{
		Owner: otherOwner, Provider: auth.SubscriptionProviderClaude,
		ExternalAccountID: "claude-other", RefreshToken: []byte("other-ciphertext"),
	})
	require.NoError(t, err)
	otherAccounts, err := repo.ListSubscriptionAccounts(ctx, otherOwner)
	require.NoError(t, err)
	require.Len(t, otherAccounts, 1)
	assert.Equal(t, otherAccount.ID, otherAccounts[0].ID)
	assert.ErrorIs(t, repo.DeleteSubscriptionAccount(ctx, otherAccount.ID, rotatedOwner), auth.ErrSubscriptionAccountNotFound)

	// Re-enrolling an existing account through the rotated key updates the same
	// row rather than creating a duplicate.
	readopted, kind, err := repo.UpsertSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{
		Owner: rotatedOwner, Provider: auth.SubscriptionProviderClaude,
		ExternalAccountID: "claude-one", RefreshToken: []byte("rotated-ciphertext"),
	})
	require.NoError(t, err)
	assert.Equal(t, auth.SubscriptionUpsertUpdated, kind, "refreshing an existing subscriber identity is not a first registration")
	assert.Equal(t, accounts[0].SubscriberID, readopted.SubscriberID)
	accounts, err = repo.ListSubscriptionAccounts(ctx, rotatedOwner)
	require.NoError(t, err)
	assert.Len(t, accounts, 3)
}

func TestSubscriptionAccountHealthIsOwnerScoped(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	repo := postgres.NewSubscriptionAccountRepo(fixture.pool)
	healthRepo := repo.(interface {
		UpdateSubscriptionAccountHealth(context.Context, string, auth.SubscriptionOwner, auth.SubscriptionAccountState, bool, *time.Time) error
	})
	ctx := context.Background()
	owner := auth.SubscriptionOwner{InstallationID: fixture.installationID.String(), SubscriberID: fixture.subscriberA.String(), APIKeyID: fixture.keyA1.String()}
	account, _, err := repo.UpsertSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{
		Owner: owner, Provider: auth.SubscriptionProviderClaude,
		ExternalAccountID: "claude-health", RefreshToken: []byte("ciphertext"),
	})
	require.NoError(t, err)
	assert.Equal(t, auth.SubscriptionAccountStateUnknown, account.State)

	resetAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	require.NoError(t, healthRepo.UpdateSubscriptionAccountHealth(
		ctx, account.ID, owner, auth.SubscriptionAccountStateExhausted, true, &resetAt,
	))
	accounts, err := repo.ListSubscriptionAccounts(ctx, owner)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	assert.Equal(t, auth.SubscriptionAccountStateExhausted, accounts[0].State)
	require.NotNil(t, accounts[0].CooldownUntil)
	assert.WithinDuration(t, resetAt, *accounts[0].CooldownUntil, time.Microsecond)

	require.NoError(t, healthRepo.UpdateSubscriptionAccountHealth(
		ctx, account.ID, owner, auth.SubscriptionAccountStateActive, true, nil,
	))
	accounts, err = repo.ListSubscriptionAccounts(ctx, owner)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	assert.Equal(t, auth.SubscriptionAccountStateExhausted, accounts[0].State)
	require.NotNil(t, accounts[0].CooldownUntil)
	assert.WithinDuration(t, resetAt, *accounts[0].CooldownUntil, time.Microsecond)

	otherOwner := auth.SubscriptionOwner{InstallationID: fixture.installationID.String(), SubscriberID: fixture.subscriberB.String(), APIKeyID: fixture.keyB1.String()}
	assert.ErrorIs(t, healthRepo.UpdateSubscriptionAccountHealth(
		ctx, account.ID, otherOwner, auth.SubscriptionAccountStateDisabled, false, nil,
	), auth.ErrSubscriptionAccountNotFound)

	require.NoError(t, repo.UpdateSubscriptionAccountState(ctx, account.ID, owner, false, nil))
	require.NoError(t, healthRepo.UpdateSubscriptionAccountHealth(
		ctx, account.ID, owner, auth.SubscriptionAccountStateActive, true, nil,
	))
	accounts, err = repo.ListSubscriptionAccounts(ctx, owner)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	assert.False(t, accounts[0].Enabled, "health writes must not re-enable an operator-disabled account")
}

func TestSubscriptionAccountsKeepLegacyAPIKeyOwnership(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	repo := postgres.NewSubscriptionAccountRepo(fixture.pool)
	ctx := context.Background()

	legacyID := fixture.legacyRow(t, fixture.legacyKey, "claude", "claude-legacy")
	legacyOwner := auth.SubscriptionOwner{APIKeyID: fixture.legacyKey.String()}

	accounts, err := repo.ListSubscriptionAccounts(ctx, legacyOwner)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	assert.Equal(t, legacyID.String(), accounts[0].ID)
	assert.Empty(t, accounts[0].SubscriberID)

	// A subscriber cannot reach another key's unattributed row.
	subscriberOwner := auth.SubscriptionOwner{InstallationID: fixture.installationID.String(), SubscriberID: fixture.subscriberA.String(), APIKeyID: fixture.keyA1.String()}
	subscriberAccounts, err := repo.ListSubscriptionAccounts(ctx, subscriberOwner)
	require.NoError(t, err)
	assert.Empty(t, subscriberAccounts)
	assert.ErrorIs(t, repo.UpdateSubscriptionAccountState(ctx, legacyID.String(), subscriberOwner, false, nil),
		auth.ErrSubscriptionAccountNotFound)

	// The legacy owner still manages its own row until it is reconnected.
	require.NoError(t, repo.UpdateSubscriptionAccountState(ctx, legacyID.String(), legacyOwner, false, nil))
	accounts, err = repo.ListSubscriptionAccounts(ctx, legacyOwner)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	assert.False(t, accounts[0].Enabled)
}

func TestSubscriptionAccountReconnectCannotAdoptLegacyRow(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	repo := postgres.NewSubscriptionAccountRepo(fixture.pool)
	ctx := context.Background()

	// A row the migration could not attribute, enrolled by a key that has since
	// gained a credential subject.
	legacyID := fixture.legacyRow(t, fixture.keyA1, "codex", "codex-legacy")
	owner := auth.SubscriptionOwner{InstallationID: fixture.installationID.String(), SubscriberID: fixture.subscriberA.String(), APIKeyID: fixture.keyA1.String()}

	adopted, kind, err := repo.UpsertSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{
		Owner: owner, Provider: auth.SubscriptionProviderCodex,
		ExternalAccountID: "codex-legacy", RefreshToken: []byte("reconnected-ciphertext"),
	})
	require.Error(t, err, "reconnect must not silently claim an unassigned account")
	assert.Nil(t, adopted)
	assert.Equal(t, auth.SubscriptionUpsertUpdated, kind)
	var persistedOwner *uuid.UUID
	var persistedToken []byte
	require.NoError(t, fixture.pool.QueryRow(ctx, "SELECT subscriber_id, refresh_token_ciphertext FROM router.model_router_subscription_accounts WHERE id = $1", legacyID).Scan(&persistedOwner, &persistedToken))
	assert.Nil(t, persistedOwner)
	assert.Equal(t, []byte("legacy-ciphertext"), persistedToken)
}

func TestSubscriptionAccountConcurrentFirstEnrollmentConverges(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	repo := postgres.NewSubscriptionAccountRepo(fixture.pool)

	// Two harness keys of one subscriber linking the same provider account at
	// once: neither sees an existing row to lock, so both reach the insert.
	owners := fixture.subscriberAKeys()
	enrolled := concurrentEnrollments(t, repo, "claude-concurrent", owners)
	assert.Equal(t, enrolled[0].ID, enrolled[1].ID, "concurrent enrollment converges on one account")

	accounts, err := repo.ListSubscriptionAccounts(context.Background(), owners[0])
	require.NoError(t, err)
	assert.Len(t, accounts, 1)
}

func TestSubscriptionAccountDuplicatePhysicalEnrollmentRejected(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	fixture.legacyRow(t, fixture.keyA1, "claude", "claude-duplicated")
	_, err := fixture.pool.Exec(context.Background(), `INSERT INTO router.model_router_subscription_accounts (api_key_id, provider, external_account_id, refresh_token_ciphertext) VALUES ($1, 'claude', 'claude-duplicated', $2)`, fixture.keyA2, []byte("duplicate"))
	require.Error(t, err, "one physical account cannot have two active identities")
}

func (f subscriptionFixture) subscriberAKeys() []auth.SubscriptionOwner {
	return []auth.SubscriptionOwner{
		{InstallationID: f.installationID.String(), SubscriberID: f.subscriberA.String(), APIKeyID: f.keyA1.String()},
		{InstallationID: f.installationID.String(), SubscriberID: f.subscriberA.String(), APIKeyID: f.keyA2.String()},
	}
}

func concurrentEnrollments(
	t *testing.T,
	repo auth.SubscriptionAccountRepository,
	externalAccountID string,
	owners []auth.SubscriptionOwner,
) []*auth.SubscriptionAccount {
	t.Helper()
	start := make(chan struct{})
	accounts := make([]*auth.SubscriptionAccount, len(owners))
	errs := make([]error, len(owners))
	var wait sync.WaitGroup
	for index, owner := range owners {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			accounts[index], _, errs[index] = repo.UpsertSubscriptionAccount(context.Background(), auth.CreateSubscriptionAccountParams{
				Owner: owner, Provider: auth.SubscriptionProviderClaude,
				ExternalAccountID: externalAccountID, RefreshToken: []byte("ciphertext"),
			})
		}()
	}
	close(start)
	wait.Wait()

	for index := range owners {
		require.NoError(t, errs[index])
		require.NotNil(t, accounts[index])
	}
	return accounts
}

func TestSubscriptionRefreshLeaseSurvivesKeyRotation(t *testing.T) {
	fixture := newSubscriptionFixture(t)
	repo := postgres.NewSubscriptionAccountRepo(fixture.pool)
	ctx := context.Background()

	enrollingOwner := auth.SubscriptionOwner{InstallationID: fixture.installationID.String(), SubscriberID: fixture.subscriberA.String(), APIKeyID: fixture.keyA1.String()}
	account, _, err := repo.UpsertSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{
		Owner: enrollingOwner, Provider: auth.SubscriptionProviderClaude,
		ExternalAccountID: "claude-lease", RefreshToken: []byte("ciphertext"),
	})
	require.NoError(t, err)

	rotatedOwner := auth.SubscriptionOwner{InstallationID: fixture.installationID.String(), SubscriberID: fixture.subscriberA.String(), APIKeyID: fixture.keyA2.String()}
	leaseID := uuid.NewString()
	acquisition, err := repo.TryAcquireSubscriptionRefreshLease(ctx, account.ID, rotatedOwner, leaseID, time.Minute)
	require.NoError(t, err)
	require.True(t, acquisition.Acquired)
	assert.False(t, acquisition.TookOver)

	// Another subscriber's key cannot lease or read the account.
	foreignOwner := auth.SubscriptionOwner{InstallationID: fixture.installationID.String(), SubscriberID: fixture.subscriberB.String(), APIKeyID: fixture.keyB1.String()}
	foreign, err := repo.TryAcquireSubscriptionRefreshLease(ctx, account.ID, foreignOwner, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	assert.False(t, foreign.Acquired)
	_, err = repo.GetSubscriptionCredentialRecord(ctx, account.ID, foreignOwner)
	assert.ErrorIs(t, err, auth.ErrSubscriptionAccountNotFound)

	record, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, rotatedOwner)
	require.NoError(t, err)
	require.Equal(t, leaseID, record.TokenRefreshLeaseID)

	// A stale version is rejected, and the fenced write from the lease holder wins.
	staleErr := repo.PersistSubscriptionTokens(ctx, account.ID, rotatedOwner, leaseID, record.TokenRefreshVersion+1,
		[]byte("refresh-new"), []byte("access-new"), time.Now().Add(time.Hour))
	assert.ErrorIs(t, staleErr, auth.ErrSubscriptionRefreshConflict)
	require.NoError(t, repo.PersistSubscriptionTokens(ctx, account.ID, rotatedOwner, leaseID, record.TokenRefreshVersion,
		[]byte("refresh-new"), []byte("access-new"), time.Now().Add(time.Hour)))

	persisted, err := repo.GetSubscriptionCredentialRecord(ctx, account.ID, enrollingOwner)
	require.NoError(t, err)
	assert.Equal(t, []byte("refresh-new"), persisted.RefreshTokenCiphertext)
	assert.Equal(t, record.TokenRefreshVersion+1, persisted.TokenRefreshVersion)
	assert.Empty(t, persisted.TokenRefreshLeaseID, "persisting releases the lease")
}
