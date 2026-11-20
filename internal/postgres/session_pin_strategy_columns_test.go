package postgres_test

import (
	"context"
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

func TestSessionPinRepo_StrategyColumnsRoundTripThroughAccessor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	installationID := uuid.New()
	_, err := pool.Exec(ctx, "INSERT INTO router.model_router_installations (id, external_id, name) VALUES ($1, $2, $2)", installationID, installationID.String()[:30])
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM router.model_router_installations WHERE id = $1", installationID)
	})

	repo := postgres.NewSessionPinRepo(pool, pinstate.Accessor{})
	var key [sessionpin.SessionKeyLen]byte
	copy(key[:], uuid.New().String())
	columns := sessionpin.StrategyColumns{PolicyGroup: "g3", ConsecutiveDowngradeVotes: 2, ConsecutiveUpgradeVotes: 1}
	pin := pinstate.WithColumns(sessionpin.Pin{
		SessionKey: key, Role: sessionpin.DefaultRole, InstallationID: installationID,
		Provider: "openai", Model: "gpt-6-luna", Reason: "test", Strategy: router.StrategyHMMEmbedding,
		TurnCount: 1, PinnedUntil: time.Now().Add(time.Hour),
	}, columns)
	require.NoError(t, repo.Upsert(ctx, pin))

	got, found, err := repo.Get(ctx, key, pin.Role)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, columns, pinstate.Columns(got))

	var policyGroup string
	var downgradeVotes, upgradeVotes int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT policy_group, consecutive_downgrade_votes, consecutive_upgrade_votes FROM router.session_pins WHERE session_key = $1 AND role = $2",
		key[:], pin.Role).Scan(&policyGroup, &downgradeVotes, &upgradeVotes))
	assert.Equal(t, sessionpin.StrategyColumns{PolicyGroup: policyGroup, ConsecutiveDowngradeVotes: downgradeVotes, ConsecutiveUpgradeVotes: upgradeVotes}, columns)

	consumed, found, err := repo.Consume(ctx, key, pin.Role, router.StrategyHMMEmbedding)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, columns, pinstate.Columns(consumed))
}
