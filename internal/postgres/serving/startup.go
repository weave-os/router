package serving

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
)

// WarmDatabase exercises serving schema and decoding on one retained pool connection.
// The reserved, non-hash lookup cannot authenticate or select a customer credential.
func WarmDatabase(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire startup database connection: %w", err)
	}
	defer conn.Release()
	return warmDatabaseQueries(ctx, dbbudget.Queries(conn))
}

func warmDatabaseQueries(ctx context.Context, queries *sqlc.Queries) error {
	_, err := queries.GetActiveModelRouterAPIKeyWithInstallationByHash(ctx, "__router_startup_no_credential__")
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("exercise credential schema: %w", err)
	}
	clock, err := queries.GetServingAdmissionClock(ctx)
	if err != nil {
		return fmt.Errorf("exercise serving clock: %w", err)
	}
	if !clock.Valid || clock.Time.IsZero() {
		return errors.New("serving clock returned no timestamp")
	}
	return nil
}
