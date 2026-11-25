package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/postgres"
)

func TestBlindExperimentControlModelLoadsForSessionInstallation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	installationID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO router.model_router_installations (id, external_id, name) VALUES ($1, $2, $2)`,
		installationID, installationID.String()[:30])
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM router.model_router_installations WHERE id = $1", installationID)
	})
	_, err = pool.Exec(ctx,
		`INSERT INTO router.blind_router_experiment_configurations
		   (installation_id, organization_id, enabled, router_on_percentage, seed, session_mode, control_model)
		 VALUES ($1, 'org-control', TRUE, 50, gen_random_uuid(), TRUE, 'claude-opus-5-5')`,
		installationID)
	require.NoError(t, err)

	record, err := postgres.NewBlindExperimentSessionRepo(pool).GetForInstallation(ctx, installationID.String())

	require.NoError(t, err)
	assert.True(t, record.SessionMode)
	assert.Equal(t, "claude-opus-5-5", record.ControlModel)

	_, err = pool.Exec(ctx,
		`UPDATE router.blind_router_experiment_configurations SET control_model = 'weave-auto' WHERE installation_id = $1`,
		installationID)
	require.NoError(t, err)
	record, err = postgres.NewBlindExperimentSessionRepo(pool).GetForInstallation(ctx, installationID.String())
	require.NoError(t, err)
	assert.Empty(t, record.ControlModel, "an unservable control model falls back to the detected baseline")

	_, err = pool.Exec(ctx,
		`UPDATE router.blind_router_experiment_configurations SET control_model = '  ' WHERE installation_id = $1`,
		installationID)
	assert.Error(t, err, "a blank control model must be NULL, not whitespace")
}
