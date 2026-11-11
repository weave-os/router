package postgres_test

import (
	"context"
	"errors"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/postgres"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scanResult struct {
	value int64
	err   error
}

func (r scanResult) Scan(destinations ...any) error {
	if r.err != nil {
		return r.err
	}
	*(destinations[0].(*int64)) = r.value
	return nil
}

type billingDBStub struct {
	rows      []pgx.Row
	queryArgs [][]any
}

func (s *billingDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("unexpected Exec")
}

func (s *billingDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected Query")
}

func (s *billingDBStub) QueryRow(_ context.Context, _ string, arguments ...any) pgx.Row {
	s.queryArgs = append(s.queryArgs, arguments)
	row := s.rows[0]
	s.rows = s.rows[1:]
	return row
}

func TestBillingRepoClassifiesBalanceFloorRejection(t *testing.T) {
	database := &billingDBStub{rows: []pgx.Row{
		scanResult{err: pgx.ErrNoRows},
		scanResult{value: -900_000_000},
	}}
	repository := postgres.NewBillingRepo(database)

	balance, err := repository.DebitInference(context.Background(), billing.DebitParams{
		OrganizationID:     "org_x",
		DeltaUsdMicros:     -200_000_000,
		NotionalCostMicros: 200_000_000,
		EntryType:          billing.EntryTypeInference,
	})

	require.ErrorIs(t, err, billing.ErrBalanceFloorExceeded)
	assert.Equal(t, int64(-900_000_000), balance)
	require.Len(t, database.queryArgs, 2)
	assert.Contains(t, database.queryArgs[0], billing.OrganizationBalanceFloorMicros)
}

func TestBillingRepoStillClassifiesMissingBalanceRow(t *testing.T) {
	database := &billingDBStub{rows: []pgx.Row{
		scanResult{err: pgx.ErrNoRows},
		scanResult{err: pgx.ErrNoRows},
	}}
	repository := postgres.NewBillingRepo(database)

	_, err := repository.DebitInference(context.Background(), billing.DebitParams{
		OrganizationID: "org_missing",
		EntryType:      billing.EntryTypeInference,
	})

	assert.ErrorIs(t, err, billing.ErrBalanceRowMissing)
	assert.False(t, errors.Is(err, billing.ErrBalanceFloorExceeded))
}
