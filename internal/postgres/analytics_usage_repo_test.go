package postgres_test

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/analytics"
	"weave-os/router/internal/postgres"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	usageOrgID             = "usage-repo-test-org"
	usageOtherOrgID        = "usage-repo-test-other"
	usageInstallation      = "7a000000-0000-0000-0000-000000000001"
	usageOtherInstallation = "7a000000-0000-0000-0000-000000000002"
	usageUserID            = "7a000000-0000-0000-0000-0000000000a1"
	usageAPIKeyID          = "7a000000-0000-0000-0000-0000000000b1"
)

// seedUsageFixture inserts inside tx so the caller's rollback leaves no rows.
func seedUsageFixture(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	statements := []string{
		`INSERT INTO router.model_router_installations (id, external_id, name) VALUES
			('` + usageInstallation + `', '` + usageOrgID + `', 'usage'),
			('` + usageOtherInstallation + `', '` + usageOtherOrgID + `', 'other')`,
		`INSERT INTO router.model_router_users (id, installation_id, email, display_name) VALUES
			('` + usageUserID + `', '` + usageInstallation + `', 'dev@example.com', 'Dev')`,
		`INSERT INTO router.model_router_api_keys (id, installation_id, external_id, name, key_prefix, key_hash, key_suffix, scope) VALUES
			('` + usageAPIKeyID + `', '` + usageInstallation + `', 'ext', 'ci-key', 'rk_', 'usage-test-hash', '0000', 'routing')`,
		`INSERT INTO router.model_router_request_telemetry
			(installation_id, request_id, trace_id, span_type, created_at, timestamp, router_user_id, api_key_id,
			 decision_model, decision_provider, requested_model, input_tokens, output_tokens, cache_read_tokens,
			 cache_creation_tokens, actual_input_cost_usd, actual_output_cost_usd, credential_source, upstream_status_code, error_class)
		VALUES
			('` + usageInstallation + `', 'u1', 't1', 'router.upstream', '2026-11-20 10:00+00', '2026-11-20 10:00+00', '` + usageUserID + `', '` + usageAPIKeyID + `', 'm-a', 'p-a', 'req-a', 10, 5, 2, 3, 1000000, 500000, NULL, 200, NULL),
			('` + usageInstallation + `', 'u2', 't2', 'router.upstream', '2026-11-20 11:00+00', '2026-11-20 11:00+00', '` + usageUserID + `', '` + usageAPIKeyID + `', 'm-a', 'p-a', 'req-a', 1, 1, 0, 0, 200000, 0, 'subscription', 200, NULL),
			('` + usageInstallation + `', 'u3', 't3', 'router.upstream', '2026-11-21 09:00+00', '2026-11-21 09:00+00', '` + usageUserID + `', NULL, 'm-b', 'p-b', 'req-b', 4, 0, 0, 0, 300000, 0, NULL, 500, 'upstream_error'),
			('` + usageInstallation + `', 'u4', 't4', 'router.auxiliary_inference', '2026-11-20 12:00+00', '2026-11-20 12:00+00', NULL, NULL, 'm-x', 'p-x', 'req-x', 7, 7, 0, 0, 9999999, 0, NULL, 200, NULL),
			('` + usageInstallation + `', 'u5', 't5', 'router.upstream', '2026-12-05 09:00+00', '2026-12-05 09:00+00', '` + usageUserID + `', NULL, 'm-a', 'p-a', 'req-a', 1, 1, 0, 0, 111, 0, NULL, 200, NULL),
			('` + usageOtherInstallation + `', 'u6', 't6', 'router.upstream', '2026-11-20 13:00+00', '2026-11-20 13:00+00', NULL, NULL, 'm-o', 'p-o', 'req-o', 1, 1, 0, 0, 777777, 0, NULL, 200, NULL)`,
	}
	for _, statement := range statements {
		_, err := tx.Exec(ctx, statement)
		require.NoError(t, err)
	}
}

func usageQuery(groups ...analytics.UsageGroup) analytics.UsageQuery {
	set := make(map[analytics.UsageGroup]struct{}, len(groups))
	for _, g := range groups {
		set[g] = struct{}{}
	}
	return analytics.UsageQuery{
		OrganizationID: usageOrgID,
		Start:          time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		End:            time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC),
		Groups:         set,
		Limit:          100,
	}
}

func TestUsageRepoAggregatesWithinOrgAndWindow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	seedUsageFixture(t, ctx, tx)
	repo := postgres.NewUsageRepo(tx)

	rows, err := repo.GetUsageRows(ctx, usageQuery(analytics.UsageGroups...))
	require.NoError(t, err)
	require.Len(t, rows, 2, "aux spans, the other org, and December rows are excluded")

	first := rows[0]
	assert.Equal(t, "2026-11-20", first.Date)
	require.NotNil(t, first.Email)
	assert.Equal(t, "dev@example.com", *first.Email)
	require.NotNil(t, first.APIKeyName)
	assert.Equal(t, "ci-key", *first.APIKeyName)
	require.NotNil(t, first.ServedProvider)
	assert.Equal(t, "p-a", *first.ServedProvider)
	assert.Equal(t, int64(2), first.Requests)
	assert.Equal(t, int64(1), first.SubscriptionRequests)
	assert.Equal(t, int64(0), first.FailedRequests)
	assert.Equal(t, int64(11), first.InputTokens)
	assert.Equal(t, int64(3), first.CacheWriteTokens)
	assert.Equal(t, int64(1_500_000), first.CostUSDMicros, "subscription-served cost is zero")
	assert.Equal(t, int64(1_700_000), first.APIRateCostUSDMicros)

	second := rows[1]
	assert.Equal(t, "2026-11-21", second.Date)
	assert.Nil(t, second.APIKeyID)
	assert.Equal(t, int64(1), second.FailedRequests)
}

func TestUsageRepoCollapsesUngroupedDimensionsAndFilters(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	seedUsageFixture(t, ctx, tx)
	repo := postgres.NewUsageRepo(tx)

	rows, err := repo.GetUsageRows(ctx, usageQuery())
	require.NoError(t, err)
	require.Len(t, rows, 2, "one row per date when only date is grouped")
	assert.Nil(t, rows[0].Email)
	assert.Nil(t, rows[0].ServedModel)
	assert.Equal(t, int64(2), rows[0].Requests)

	filtered := usageQuery(analytics.UsageGroupAPIKey)
	filtered.APIKeyIDs = []string{usageAPIKeyID}
	rows, err = repo.GetUsageRows(ctx, filtered)
	require.NoError(t, err)
	require.Len(t, rows, 1, "the api key filter drops the keyless November 21 row")

	paged := usageQuery()
	paged.Limit = 1
	paged.Cursor = analytics.UsageCursor{Offset: 1}
	rows, err = repo.GetUsageRows(ctx, paged)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "2026-11-21", rows[0].Date)

	frozen := usageQuery()
	frozen.Cursor = analytics.UsageCursor{Offset: 0, IngestedBefore: time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)}
	rows, err = repo.GetUsageRows(ctx, frozen)
	require.NoError(t, err)
	require.Len(t, rows, 1, "rows ingested after the cursor's cutoff stay out of later pages")
	assert.Equal(t, "2026-11-20", rows[0].Date)
}
