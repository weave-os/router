package postgres_test

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/postgres"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Round-trips a thread's tokenizer calibration through the session_pins
// JSONB column, and checks the write is scoped like UpdateUsage (a
// different-strategy row is untouched) and that a pin refresh preserves it.
// Gated on ROUTER_TEST_DATABASE_URL (see flag_definitions_test.go).
func TestSessionPinRepo_TokenizerCalibrationRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	installationID := uuid.New()
	_, err := pool.Exec(ctx, "INSERT INTO router.model_router_installations (id, external_id, name) VALUES ($1, $2, $2)", installationID, installationID.String()[:30])
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM router.model_router_installations WHERE id = $1", installationID)
	})

	repo := postgres.NewSessionPinRepo(pool)
	var key [sessionpin.SessionKeyLen]byte
	copy(key[:], uuid.New().String())
	pin := sessionpin.Pin{
		SessionKey: key, Role: sessionpin.DefaultRole, InstallationID: installationID,
		Provider: "openai", Model: "gpt-6-luna", Reason: "test", Strategy: router.StrategyCluster,
		TurnCount: 1, PinnedUntil: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.Upsert(ctx, pin))

	at := time.Now().UTC().Truncate(time.Second)
	cal := sessionpin.TokenizerCalibration{}.
		Observe(sessionpin.TokenizerFamilyAnthropic, 860_000, at).
		Observe(sessionpin.TokenizerFamilyOpenAI, 500_000, at.Add(time.Second))
	require.NoError(t, repo.UpdateTokenizerCalibration(ctx, key, pin.Role, router.StrategyCluster, cal))

	require.NoError(t, repo.UpdateTokenizerCalibration(ctx, key, pin.Role, router.Strategy("hmm_beta"), sessionpin.TokenizerCalibration{}),
		"a different strategy's write is a no-op")
	require.NoError(t, repo.Upsert(ctx, pin), "a pin refresh must not clear calibration")

	got, found, err := repo.Get(ctx, key, pin.Role)
	require.NoError(t, err)
	require.True(t, found)
	ratio, ok := got.TokenizerCalibration.Ratio(sessionpin.TokenizerFamilyOpenAI, sessionpin.TokenizerFamilyAnthropic)
	require.True(t, ok)
	assert.InDelta(t, 1.72, ratio, 1e-9)
	assert.True(t, got.TokenizerCalibration.Observations[sessionpin.TokenizerFamilyOpenAI].ObservedAt.Equal(at.Add(time.Second)))
}
