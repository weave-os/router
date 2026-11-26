package postgres_test

import (
	"context"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/postgres"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBillingTablesExistDetectsMissingAutopayConfig(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	ok, err := postgres.NewBillingRepo(pool).BillingTablesExist(ctx)
	require.NoError(t, err)
	require.True(t, ok, "migrated database should report all billing tables present")

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "DROP TABLE router.subscriber_autopay_config")
	require.NoError(t, err)

	ok, err = postgres.NewBillingRepo(tx).BillingTablesExist(ctx)
	require.NoError(t, err)
	assert.False(t, ok, "missing subscriber_autopay_config must fail billing readiness")
}

func TestMalformedSpendIDsEnforceNoCapWithoutError(t *testing.T) {
	repo := postgres.NewBillingRepo(nil)
	ctx := context.Background()

	spent, keyCap, found, err := repo.GetAPIKeySpend(ctx, "not-a-uuid")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, keyCap)
	assert.Zero(t, spent)

	spent, limit, granted, err := repo.GetUserMonthlySpendAndLimit(ctx, "org", "not-a-uuid")
	require.NoError(t, err)
	assert.Nil(t, limit)
	assert.Zero(t, spent)
	assert.Zero(t, granted)
}

func TestMissingSpendGrantsFailsUserGateClosedWithoutDisablingBilling(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "DROP TABLE router.model_router_user_spend_grants")
	require.NoError(t, err)

	tablesExist, err := postgres.NewBillingRepo(tx).BillingTablesExist(ctx)
	require.NoError(t, err)
	require.True(t, tablesExist, "a missing grants table must not switch billing off for the whole fleet")

	_, err = billing.CheckUserMonthlySpendLimit(ctx, billing.NewService(postgres.NewBillingRepo(tx)), "spend-grant-missing-org", "7c000000-0000-0000-0000-0000000000a1", func() bool { return false })
	assert.ErrorIs(t, err, billing.ErrSpendLimitCheckUnavailable, "the user gate fails closed for that request instead")
}
