package postgres_test

import (
	"context"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/postgres"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	grantOrgID          = "spend-grant-test-org"
	grantInstallationID = "7b000000-0000-0000-0000-000000000001"
	grantUserID         = "7b000000-0000-0000-0000-0000000000a1"
)

func TestUserMonthlySpendCountsOnlyThisMonthsGrants(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	thisMonth := "DATE_TRUNC('month', NOW() AT TIME ZONE 'utc')::date"
	for _, statement := range []string{
		`INSERT INTO router.model_router_installations (id, external_id, name) VALUES ('` + grantInstallationID + `', '` + grantOrgID + `', 'grants')`,
		`INSERT INTO router.model_router_users (id, installation_id, email) VALUES ('` + grantUserID + `', '` + grantInstallationID + `', 'grant@example.com')`,
		`INSERT INTO router.organization_spend_limits (organization_id, user_monthly_limit_usd_micros) VALUES ('` + grantOrgID + `', 1000000)`,
		`INSERT INTO router.model_router_user_monthly_spend (router_user_id, month, spent_usd_micros) VALUES ('` + grantUserID + `', ` + thisMonth + `, 1200000)`,
		`INSERT INTO router.model_router_user_spend_grants (router_user_id, month, amount_usd_micros, created_by, idempotency_key) VALUES
			('` + grantUserID + `', ` + thisMonth + `, 500000, 'test', 'grant-1'),
			('` + grantUserID + `', (` + thisMonth + ` - INTERVAL '1 month')::date, 9000000, 'test', 'grant-old')`,
	} {
		_, err := tx.Exec(ctx, statement)
		require.NoError(t, err)
	}

	repo := postgres.NewBillingRepo(tx)
	spent, limit, granted, err := repo.GetUserMonthlySpendAndLimit(ctx, grantOrgID, grantUserID)
	require.NoError(t, err)
	assert.Equal(t, int64(1_200_000), spent)
	require.NotNil(t, limit)
	assert.Equal(t, int64(1_000_000), *limit)
	assert.Equal(t, int64(500_000), granted, "last month's grant must not count")

	result, err := billing.NewService(repo).CheckUserMonthlySpend(ctx, grantOrgID, grantUserID)
	require.NoError(t, err)
	assert.False(t, result.LimitReached(), "over the base cap but under base plus this month's grant")

	_, err = tx.Exec(ctx, `INSERT INTO router.model_router_user_spend_grants (router_user_id, month, amount_usd_micros, created_by, idempotency_key)
		VALUES ('`+grantUserID+`', `+thisMonth+`, 500000, 'test', 'grant-1')`)
	require.Error(t, err, "a repeated idempotency key is rejected")
}
