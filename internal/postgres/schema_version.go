package postgres

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/sqlc"
)

const appliedSchemaVersionQuery = "SELECT version, dirty FROM router.schema_migrations LIMIT 1"

var ErrDirtySchemaVersion = errors.New("router schema migration is dirty")

func AppliedSchemaVersion(ctx context.Context, tx sqlc.DBTX) (int64, error) {
	var version int64
	var dirty bool
	if err := tx.QueryRow(ctx, appliedSchemaVersionQuery).Scan(&version, &dirty); err != nil {
		return 0, fmt.Errorf("read router schema version: %w", err)
	}
	if dirty {
		return 0, fmt.Errorf("%w at version %d", ErrDirtySchemaVersion, version)
	}
	return version, nil
}
