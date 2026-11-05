package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/sessionpin"
)

func TestSessionSiblingPreferencePostgresMergeAndIsolation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	installation := uuid.New()
	_, err := pool.Exec(ctx, "INSERT INTO router.model_router_installations (id, external_id, name) VALUES ($1,$2,$2)", installation, installation.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM router.model_router_installations WHERE id=$1", installation)
	})
	store := postgres.NewSessionPinRepo(pool)
	var key [sessionpin.SessionKeyLen]byte
	copy(key[:], uuid.New().String())
	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	primary := catalog.ModelIDClaudeOpus48.String()
	sibling := catalog.ModelIDGPT56Luna.String()
	written, err := store.RecordSiblingPreference(ctx, installation, key, router.StrategyHMM, escalation.High, sibling, []string{primary}, startedAt)
	require.NoError(t, err)
	require.True(t, written)
	var wg sync.WaitGroup
	recordErrs := make(chan error, 3)
	for _, displaced := range []string{catalog.ModelIDClaudeOpus47.String(), catalog.ModelIDClaudeSonnet46.String()} {
		wg.Go(func() {
			_, err := store.RecordSiblingPreference(ctx, installation, key, router.StrategyHMM, escalation.High, sibling, []string{displaced}, startedAt)
			recordErrs <- err
		})
	}
	wg.Go(func() {
		_, err := store.RecordSiblingPreference(ctx, installation, key, router.StrategyHMM, escalation.Medium, primary, []string{sibling}, startedAt)
		recordErrs <- err
	})
	wg.Wait()
	close(recordErrs)
	for err := range recordErrs {
		require.NoError(t, err)
	}
	store = postgres.NewSessionPinRepo(pool)
	got, err := store.GetSiblingPreferences(ctx, installation, key, router.StrategyHMM)
	require.NoError(t, err)
	assert.Equal(t, sibling, got[escalation.High].Model)
	assert.ElementsMatch(t, []string{primary, catalog.ModelIDClaudeOpus47.String(), catalog.ModelIDClaudeSonnet46.String()}, got[escalation.High].DisplacedModels)
	assert.Equal(t, primary, got[escalation.Medium].Model)
	written, err = store.RecordSiblingPreference(ctx, installation, key, router.StrategyHMM, escalation.High, primary, []string{sibling}, startedAt.Add(time.Second))
	require.NoError(t, err)
	require.True(t, written)
	written, err = store.RecordSiblingPreference(ctx, installation, key, router.StrategyHMM, escalation.High, sibling, []string{primary}, startedAt)
	require.NoError(t, err)
	require.False(t, written, "late completion must not overwrite a newer turn")
	got, err = store.GetSiblingPreferences(ctx, installation, key, router.StrategyHMM)
	require.NoError(t, err)
	assert.Equal(t, primary, got[escalation.High].Model)
	assert.NotContains(t, got[escalation.High].DisplacedModels, primary)
	assert.Contains(t, got[escalation.High].DisplacedModels, sibling)
	other := key
	other[0] ^= 1
	empty, err := store.GetSiblingPreferences(ctx, installation, other, router.StrategyHMM)
	require.NoError(t, err)
	assert.Empty(t, empty)
	empty, err = store.GetSiblingPreferences(ctx, installation, key, router.StrategyHMMBeta)
	require.NoError(t, err)
	assert.Empty(t, empty)
	empty, err = store.GetSiblingPreferences(ctx, uuid.New(), key, router.StrategyHMM)
	require.NoError(t, err)
	assert.Empty(t, empty)
	// Pin sweeping and compaction do not remove conversation-level preferences.
	pin := sessionpin.Pin{SessionKey: key, Role: sessionpin.DefaultRole, InstallationID: installation, Provider: providers.ProviderAnthropic, Model: primary, Strategy: router.StrategyHMM, PinnedUntil: time.Now().Add(-48 * time.Hour)}
	require.NoError(t, store.Upsert(ctx, pin))
	require.NoError(t, store.ExpireAndDemoteModel(ctx, pin, primary, sessionpin.DemotionReasonRescuedFailure))
	require.NoError(t, store.ExpireAndCoolDownModel(ctx, pin, primary, time.Now().Add(time.Hour), sessionpin.DemotionReasonRateLimited))
	require.NoError(t, store.StrikeTurnModel(ctx, key, pin.Role, primary))
	require.NoError(t, store.ClearSiblingWinnerExclusions(ctx, key, pin.Strategy, []string{pin.Role}, primary, startedAt.Add(-time.Minute)))
	newerFailure, _, err := store.Get(ctx, key, pin.Role)
	require.NoError(t, err)
	assert.Contains(t, newerFailure.DemotedModels, primary, "an older success cannot remove a later demotion")
	assert.Contains(t, newerFailure.DemotionCooldowns, primary, "an older success cannot remove a later cooldown")
	assert.Contains(t, newerFailure.TurnStrikes, primary, "an older success cannot remove a later turn strike")
	require.NoError(t, store.ClearSiblingWinnerExclusions(ctx, key, pin.Strategy, []string{pin.Role}, primary, time.Now().Add(time.Minute)))
	cleared, found, err := store.Get(ctx, key, pin.Role)
	require.NoError(t, err)
	require.True(t, found)
	assert.NotContains(t, cleared.DemotedModels, primary)
	assert.NotContains(t, cleared.DemotionCooldowns, primary)
	assert.NotContains(t, cleared.TurnStrikes, primary)
	require.NoError(t, store.SweepExpired(ctx))
	_, found, err = store.Get(ctx, key, pin.Role)
	require.NoError(t, err)
	assert.False(t, found)
	got, err = store.GetSiblingPreferences(ctx, installation, key, router.StrategyHMM)
	require.NoError(t, err)
	assert.Equal(t, primary, got[escalation.High].Model)
}
