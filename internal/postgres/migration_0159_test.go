package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"weave-os/router/internal/postgres"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/pinstate"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigration0159RetiresClusterAndLoopEscalationState(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	migration, err := os.ReadFile("../../db/migrations/0159_retire-cluster-strategy-and-retired-overrides.up.sql")
	require.NoError(t, err)

	installationID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO router.model_router_installations (id, external_id, name, routing_strategy, policy_shadow_strategy, flag_overrides)
		 VALUES ($1, $2, $2, ' Cluster', E'CLUSTER\t', '{"loop_escalation_enabled": "true", "struggle_shadow_enabled": "false", "live_key": "1"}')`,
		installationID, installationID.String()[:30])
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM router.model_router_installations WHERE id = $1", installationID)
	})

	repo := postgres.NewSessionPinRepo(pool, pinstate.Accessor{})
	seedPin := func(strategy router.Strategy, reason string) [sessionpin.SessionKeyLen]byte {
		var key [sessionpin.SessionKeyLen]byte
		copy(key[:], uuid.New().String())
		require.NoError(t, repo.Upsert(ctx, sessionpin.Pin{
			SessionKey: key, Role: sessionpin.DefaultRole, InstallationID: installationID,
			Provider: "openai", Model: "gpt-6-luna", Reason: reason, Strategy: strategy,
			TurnCount: 1, PinnedUntil: time.Now().Add(time.Hour),
		}))
		return key
	}
	clusterPin := seedPin(" Cluster", "force_model_history")
	loopPin := seedPin(router.StrategyHMMEmbedding, "loop_escalation")
	livePin := seedPin(router.StrategyHMMEmbedding, "test")

	for range 2 {
		_, err = pool.Exec(ctx, string(migration))
		require.NoError(t, err)
	}

	for key, wantKept := range map[[sessionpin.SessionKeyLen]byte]bool{clusterPin: false, loopPin: false, livePin: true} {
		var count int
		require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM router.session_pins WHERE session_key = $1", key[:]).Scan(&count))
		assert.Equal(t, wantKept, count == 1)
	}

	var routingStrategy, shadowStrategy *string
	var overrides map[string]string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT routing_strategy, policy_shadow_strategy, flag_overrides FROM router.model_router_installations WHERE id = $1",
		installationID).Scan(&routingStrategy, &shadowStrategy, &overrides))
	assert.Nil(t, routingStrategy)
	assert.Nil(t, shadowStrategy)
	assert.Equal(t, map[string]string{"live_key": "1"}, overrides)
}
