package postgres_test

import (
	"context"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/postgres"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The point of these is the property no unit test can prove: that the SQL
// itself keeps one subscriber's prepaid money out of reach of everyone else
// who shares their organization. Gated on ROUTER_TEST_DATABASE_URL like the
// other database-backed tests here.

// seedFundedSubscriber creates a credential subject inside the given
// installation (the organization's router-side row), gives it a personal
// routing key so it is a real member of that organization, and funds its
// prepaid balance.
func seedFundedSubscriber(t *testing.T, pool *pgxpool.Pool, installationID uuid.UUID, balanceMicros int64) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	var subscriberID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO router.credential_subjects (projection_complete) VALUES (true) RETURNING id`,
	).Scan(&subscriberID))

	_, err := pool.Exec(ctx,
		`INSERT INTO router.credential_subject_installations (subject_id, installation_id, access_enabled)
		 VALUES ($1, $2, true)`, subscriberID, installationID)
	require.NoError(t, err)

	keySuffix := subscriberID.String()[:4]
	_, err = pool.Exec(ctx,
		`INSERT INTO router.model_router_api_keys
		     (installation_id, external_id, name, key_prefix, key_hash, key_suffix, scope, credential_subject_id)
		 VALUES ($1, $2, 'personal', 'wv_', $2, $3, 'routing', $4)`,
		installationID, subscriberID.String(), keySuffix, subscriberID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx,
		`INSERT INTO router.subscriber_credit_balance (subscriber_id, balance_usd_micros) VALUES ($1, $2)`,
		subscriberID, balanceMicros)
	require.NoError(t, err)

	t.Cleanup(func() {
		// Ledger and balance cascade from the subject; the key and the
		// membership row must go first.
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM router.model_router_api_keys WHERE credential_subject_id = $1`, subscriberID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM router.credential_subject_installations WHERE subject_id = $1`, subscriberID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM router.credential_subjects WHERE id = $1`, subscriberID)
	})
	return subscriberID
}

// seedInstallation creates the organization's router installation row.
func seedInstallation(t *testing.T, pool *pgxpool.Pool) (installationID uuid.UUID, organizationID string) {
	t.Helper()
	ctx := context.Background()
	organizationID = "org_" + uuid.NewString()[:24]
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO router.model_router_installations (external_id, name) VALUES ($1, 'prepaid isolation test') RETURNING id`,
		organizationID,
	).Scan(&installationID))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM router.model_router_installations WHERE id = $1`, installationID)
	})
	return installationID, organizationID
}

func subscriberLedgerCount(t *testing.T, pool *pgxpool.Pool, subscriberID uuid.UUID) int {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM router.subscriber_credit_ledger WHERE subscriber_id = $1`, subscriberID,
	).Scan(&count))
	return count
}

func TestSubscriberCreditDebitIsolatedWithinOneOrganization(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	installationID, _ := seedInstallation(t, pool)
	subscriberA := seedFundedSubscriber(t, pool, installationID, 3_000_000)
	subscriberB := seedFundedSubscriber(t, pool, installationID, 7_000_000)

	repo := postgres.NewSubscriberCreditRepo(pool)
	after, err := repo.Debit(ctx, billing.PrepaidDebit{
		Owner:              billing.SubscriberOwner(subscriberA.String()),
		DeltaUsdMicros:     -1_250_000,
		NotionalCostMicros: 1_250_000,
		EntryType:          billing.EntryTypeInference,
		RouterRequestID:    "req_isolation",
		RouterModel:        "claude-sonnet-4",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1_750_000), after)

	balanceB, err := repo.Balance(ctx, billing.SubscriberOwner(subscriberB.String()))
	require.NoError(t, err)
	assert.Equal(t, int64(7_000_000), balanceB, "colleague's prepaid balance must not move")

	assert.Equal(t, 1, subscriberLedgerCount(t, pool, subscriberA))
	assert.Equal(t, 0, subscriberLedgerCount(t, pool, subscriberB), "colleague must have no ledger entry")
}

func TestSubscriberCreditRejectsNonSubscriberOwners(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	installationID, organizationID := seedInstallation(t, pool)
	subscriberA := seedFundedSubscriber(t, pool, installationID, 3_000_000)

	repo := postgres.NewSubscriberCreditRepo(pool)

	// The organization these subscribers belong to cannot address the
	// subscriber book at all.
	_, err := repo.Balance(ctx, billing.OrganizationOwner(organizationID))
	assert.ErrorIs(t, err, billing.ErrOwnerKindUnsupported)

	// Nor can an organization id be smuggled through as a subscriber id.
	_, err = repo.Debit(ctx, billing.PrepaidDebit{
		Owner:          billing.SubscriberOwner(organizationID),
		DeltaUsdMicros: -1_000_000,
		EntryType:      billing.EntryTypeInference,
	})
	assert.ErrorIs(t, err, billing.ErrInvalidOwner)

	balanceA, err := repo.Balance(ctx, billing.SubscriberOwner(subscriberA.String()))
	require.NoError(t, err)
	assert.Equal(t, int64(3_000_000), balanceA)
}

func TestSubscriberCreditBalanceMissingRow(t *testing.T) {
	pool := testPool(t)

	repo := postgres.NewSubscriberCreditRepo(pool)
	_, err := repo.Balance(context.Background(), billing.SubscriberOwner(uuid.NewString()))
	assert.ErrorIs(t, err, billing.ErrBalanceRowMissing)
}
