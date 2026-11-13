package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/sqlc"
	"weave-os/router/internal/subscriptions/entitlement"
)

type subscriberEntitlementDB struct {
	rows    []pgx.Row
	queries []string
	args    [][]any
}

func (*subscriberEntitlementDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec call")
}

func (*subscriberEntitlementDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query call")
}

func (db *subscriberEntitlementDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	db.queries = append(db.queries, query)
	db.args = append(db.args, args)
	row := db.rows[0]
	db.rows = db.rows[1:]
	return row
}

type subscriberEntitlementRow struct {
	value sqlc.RouterSubscriberEntitlement
	err   error
}

func (row subscriberEntitlementRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	*dest[0].(*uuid.UUID) = row.value.SubscriberID
	*dest[1].(*int64) = row.value.Version
	*dest[2].(*string) = row.value.Plan
	*dest[3].(*string) = row.value.Status
	*dest[4].(*pgtype.Timestamptz) = row.value.BillingPeriodStart
	*dest[5].(*pgtype.Timestamptz) = row.value.BillingPeriodEnd
	*dest[6].(*pgtype.Timestamptz) = row.value.EffectiveAt
	*dest[7].(*int64) = row.value.MonthlyAllowanceUsdMicros
	*dest[8].(*int64) = row.value.NominalMonthlyAllowanceUsdMicros
	*dest[9].(*int64) = row.value.SixHourAllowanceUsdMicros
	*dest[10].(*bool) = row.value.AutoTopUpEnabled
	*dest[11].(*pgtype.Timestamptz) = row.value.ProjectedAt
	*dest[12].(*pgtype.Timestamptz) = row.value.CreatedAt
	*dest[13].(*pgtype.Timestamptz) = row.value.UpdatedAt
	return nil
}

func TestSubscriberEntitlementRepoProjectUsesCredentialSubjectIdentity(t *testing.T) {
	t.Parallel()

	projected := subscriberEntitlementFixture(3)
	row := subscriberEntitlementSQLRow(projected)
	db := &subscriberEntitlementDB{rows: []pgx.Row{subscriberEntitlementRow{value: row}}}
	repo := NewSubscriberEntitlementRepo(db)

	require.NoError(t, repo.Project(context.Background(), projected))
	require.Len(t, db.queries, 1)
	assert.Contains(t, db.queries[0], "ON CONFLICT (subscriber_id)")
	require.Len(t, db.args[0], 12)
	assert.Equal(t, row.SubscriberID, db.args[0][0])
	assert.Equal(t, projected.Version, db.args[0][1])
	assert.Equal(t, string(entitlement.PlanMax), db.args[0][2])
}

func TestSubscriberEntitlementRepoProjectClassifiesRejectedVersions(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		storedVersion int64
		want          error
	}{
		{name: "stale", storedVersion: 4, want: entitlement.ErrStaleProjection},
		{name: "same version conflict", storedVersion: 3, want: entitlement.ErrProjectionConflict},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			projected := subscriberEntitlementFixture(3)
			stored := subscriberEntitlementSQLRow(projected)
			stored.Version = test.storedVersion
			db := &subscriberEntitlementDB{rows: []pgx.Row{
				subscriberEntitlementRow{err: pgx.ErrNoRows},
				subscriberEntitlementRow{value: stored},
			}}
			repo := NewSubscriberEntitlementRepo(db)

			require.ErrorIs(t, repo.Project(context.Background(), projected), test.want)
			require.Len(t, db.queries, 2)
			assert.Contains(t, db.queries[1], "WHERE subscriber_id = $1::uuid")
		})
	}
}

func TestSubscriberEntitlementRepoGetMapsProjectionAndMissingRow(t *testing.T) {
	t.Parallel()

	projected := subscriberEntitlementFixture(8)
	db := &subscriberEntitlementDB{rows: []pgx.Row{subscriberEntitlementRow{value: subscriberEntitlementSQLRow(projected)}}}
	repo := NewSubscriberEntitlementRepo(db)

	got, err := repo.Get(context.Background(), projected.SubscriberID)
	require.NoError(t, err)
	assert.Equal(t, projected, got)

	db.rows = []pgx.Row{subscriberEntitlementRow{err: pgx.ErrNoRows}}
	_, err = repo.Get(context.Background(), projected.SubscriberID)
	require.ErrorIs(t, err, entitlement.ErrEntitlementNotFound)
}

func TestSubscriberEntitlementRepoRejectsInvalidDomainAndStoredValues(t *testing.T) {
	t.Parallel()

	db := &subscriberEntitlementDB{}
	repo := NewSubscriberEntitlementRepo(db)
	projected := subscriberEntitlementFixture(1)
	projected.SubscriberID = "not-a-credential-subject"
	require.ErrorIs(t, repo.Project(context.Background(), projected), entitlement.ErrInvalidContract)
	assert.Empty(t, db.queries)

	projected = subscriberEntitlementFixture(1)
	stored := subscriberEntitlementSQLRow(projected)
	stored.Plan = "enterprise"
	db.rows = []pgx.Row{subscriberEntitlementRow{value: stored}}
	_, err := repo.Get(context.Background(), projected.SubscriberID)
	require.ErrorIs(t, err, entitlement.ErrInvalidContract)
}

func subscriberEntitlementFixture(version int64) entitlement.Entitlement {
	start := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	return entitlement.Entitlement{
		SubscriberID:                     "81cb9bc0-fb29-4ff5-9720-b8de95f63220",
		Version:                          version,
		Plan:                             entitlement.PlanMax,
		Status:                           entitlement.StatusActive,
		BillingPeriod:                    entitlement.Period{Kind: entitlement.PeriodKindBilling, Start: start, End: start.AddDate(0, 1, 0)},
		EffectiveAt:                      start,
		MonthlyAllowanceUsdMicros:        50_000_000,
		NominalMonthlyAllowanceUsdMicros: 50_000_000,
		SixHourAllowanceUsdMicros:        403_225,
		AutoTopUpEnabled:                 true,
		ProjectedAt:                      start.Add(time.Minute),
	}
}

func subscriberEntitlementSQLRow(projected entitlement.Entitlement) sqlc.RouterSubscriberEntitlement {
	return sqlc.RouterSubscriberEntitlement{
		SubscriberID:                     uuid.MustParse(string(projected.SubscriberID)),
		Version:                          projected.Version,
		Plan:                             string(projected.Plan),
		Status:                           string(projected.Status),
		BillingPeriodStart:               pgtype.Timestamptz{Time: projected.BillingPeriod.Start.In(time.Local), Valid: true},
		BillingPeriodEnd:                 pgtype.Timestamptz{Time: projected.BillingPeriod.End.In(time.Local), Valid: true},
		EffectiveAt:                      pgtype.Timestamptz{Time: projected.EffectiveAt.In(time.Local), Valid: true},
		MonthlyAllowanceUsdMicros:        projected.MonthlyAllowanceUsdMicros,
		NominalMonthlyAllowanceUsdMicros: projected.NominalMonthlyAllowanceUsdMicros,
		SixHourAllowanceUsdMicros:        projected.SixHourAllowanceUsdMicros,
		AutoTopUpEnabled:                 projected.AutoTopUpEnabled,
		ProjectedAt:                      pgtype.Timestamptz{Time: projected.ProjectedAt.In(time.Local), Valid: true},
		CreatedAt:                        pgtype.Timestamptz{Time: projected.ProjectedAt, Valid: true},
		UpdatedAt:                        pgtype.Timestamptz{Time: projected.ProjectedAt, Valid: true},
	}
}
