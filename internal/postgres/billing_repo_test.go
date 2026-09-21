package postgres_test

import (
	"context"
	"testing"

	"weave-os/router/internal/postgres"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A router booted before migration 0102 lands passes the table-count check only
// if subscriber_autopay_config is counted: without it the prepaid settlement
// path's GetSubscriberAutopayConfig read fails and the recharge signal is
// silently dropped.
func TestBillingTablesExistDetectsMissingAutopayConfig(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	ok, err := postgres.NewBillingRepo(pool).BillingTablesExist(ctx)
	require.NoError(t, err)
	require.True(t, ok, "migrated database should report all billing tables present")

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "DROP TABLE router.subscriber_autopay_config")
	require.NoError(t, err)

	ok, err = postgres.NewBillingRepo(tx).BillingTablesExist(ctx)
	require.NoError(t, err)
	assert.False(t, ok, "missing subscriber_autopay_config must fail billing readiness")
}
