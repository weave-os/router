package serving

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/sqlc"
)

type startupDB struct {
	sqlc.DBTX
	credentialError, errorClock error
	invalidClock                bool
}

func (db startupDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if len(args) > 0 {
		return startupRow{err: db.credentialError}
	}
	return startupRow{err: db.errorClock, clock: !db.invalidClock}
}

type startupRow struct {
	err   error
	clock bool
}

func (r startupRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if r.clock {
		*dest[0].(*pgtype.Timestamptz) = pgtype.Timestamptz{Valid: true, Time: time.Now()}
	}
	return nil
}
func TestStartupDatabaseRequiresRealSchemaAndClock(t *testing.T) {
	missingSchema := errors.New("required relation missing")
	cases := []struct {
		name    string
		db      startupDB
		want    error
		message string
	}{
		{name: "absent credential is successful", db: startupDB{credentialError: pgx.ErrNoRows}},
		{name: "schema missing", db: startupDB{credentialError: missingSchema}, want: missingSchema},
		{name: "clock unavailable", db: startupDB{credentialError: pgx.ErrNoRows, errorClock: context.DeadlineExceeded}, want: context.DeadlineExceeded},
		{name: "clock not decoded", db: startupDB{credentialError: pgx.ErrNoRows, invalidClock: true}, message: "no timestamp"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := warmDatabaseQueries(context.Background(), sqlc.New(test.db))
			if test.want != nil {
				require.ErrorIs(t, err, test.want)
			} else if test.message != "" {
				require.ErrorContains(t, err, test.message)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
