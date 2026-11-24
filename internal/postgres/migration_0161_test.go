package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigration0161RetiresTextRepetitionBreakOverride(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	migration, err := os.ReadFile("../../db/migrations/0161_retire-text-repetition-break-override.up.sql")
	require.NoError(t, err)

	installationID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO router.model_router_installations (id, external_id, name, flag_overrides)
		 VALUES ($1, $2, $2, '{"text_repetition_break_enabled": false, "live_key": "1"}')`,
		installationID, installationID.String()[:30])
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM router.model_router_installations WHERE id = $1", installationID)
	})

	for range 2 {
		_, err = pool.Exec(ctx, string(migration))
		require.NoError(t, err)
	}

	var overrides map[string]string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT flag_overrides FROM router.model_router_installations WHERE id = $1",
		installationID).Scan(&overrides))
	assert.Equal(t, map[string]string{"live_key": "1"}, overrides)
}
