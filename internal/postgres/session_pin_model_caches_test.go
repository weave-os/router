package postgres_test

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/postgres"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/pinstate"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionPinRepo_UpdateUsageRecordsEachServedModelsCache(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	installationID := uuid.New()
	_, err := pool.Exec(ctx,
		"INSERT INTO router.model_router_installations (id, external_id, name) VALUES ($1, $2, $2)",
		installationID, installationID.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"DELETE FROM router.model_router_installations WHERE id = $1", installationID)
	})

	repo := postgres.NewSessionPinRepo(pool, pinstate.Accessor{})
	id := uuid.New()
	var key [sessionpin.SessionKeyLen]byte
	copy(key[:], id[:sessionpin.SessionKeyLen])
	pin := sessionpin.Pin{
		SessionKey: key, Role: sessionpin.DefaultRole, InstallationID: installationID,
		Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Reason: "test",
		Strategy: router.StrategyHMMEmbedding, TurnCount: 1, PinnedUntil: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.Upsert(ctx, pin))
	first := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	second := first.Add(30 * time.Second)
	require.NoError(t, repo.UpdateUsage(ctx, key, pin.Role, sessionpin.Usage{
		Strategy: router.StrategyHMMEmbedding, EndedAt: first, ServedModel: "claude-opus-4-8", ServedProvider: providers.ProviderAnthropic,
		InputTokens: 100, CachedReadTokens: 90_000, CachedWriteTokens: 9_900, PromptTokens: 100_000, MessageCount: 40, CacheWriteTTL: time.Hour,
	}))
	require.NoError(t, repo.UpdateUsage(ctx, key, pin.Role, sessionpin.Usage{
		Strategy: router.StrategyHMMEmbedding, EndedAt: second, ServedModel: "claude-sonnet-4-6", ServedProvider: providers.ProviderAnthropic,
		InputTokens: 101_000, PromptTokens: 101_000, MessageCount: 42, CacheWriteTTL: 5 * time.Minute,
	}))
	require.NoError(t, repo.Upsert(ctx, pin))

	got, found, err := repo.Get(ctx, key, pin.Role)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sessionpin.ModelCaches{
		"claude-opus-4-8":   {ServedAt: first, PromptTokens: 100_000, MessageCount: 40, WriteTTLSeconds: 3600},
		"claude-sonnet-4-6": {ServedAt: second, PromptTokens: 101_000, MessageCount: 42, WriteTTLSeconds: 300},
	}, got.ModelCaches)
}

func TestSessionPinRepo_UpdateUsageKeepsTheNewestCacheUnderTheBareModel(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	installationID := uuid.New()
	_, err := pool.Exec(ctx,
		"INSERT INTO router.model_router_installations (id, external_id, name) VALUES ($1, $2, $2)",
		installationID, installationID.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"DELETE FROM router.model_router_installations WHERE id = $1", installationID)
	})

	repo := postgres.NewSessionPinRepo(pool, pinstate.Accessor{})
	id := uuid.New()
	var key [sessionpin.SessionKeyLen]byte
	copy(key[:], id[:sessionpin.SessionKeyLen])
	pin := sessionpin.Pin{
		SessionKey: key, Role: sessionpin.DefaultRole, InstallationID: installationID,
		Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Reason: "test",
		Strategy: router.StrategyHMMEmbedding, TurnCount: 1, PinnedUntil: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.Upsert(ctx, pin))
	newer := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	older := newer.Add(-30 * time.Second)
	for _, usage := range []sessionpin.Usage{
		{EndedAt: newer, PromptTokens: 120_000, MessageCount: 50},
		{EndedAt: older, PromptTokens: 100_000, MessageCount: 40},
	} {
		usage.Strategy, usage.ServedModel, usage.ServedProvider = router.StrategyHMMEmbedding, "claude-opus-4-8:high", providers.ProviderAnthropic
		usage.InputTokens = usage.PromptTokens
		require.NoError(t, repo.UpdateUsage(ctx, key, pin.Role, usage))
	}
	require.NoError(t, repo.Upsert(ctx, pin))

	got, found, err := repo.Get(ctx, key, pin.Role)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sessionpin.ModelCaches{
		"claude-opus-4-8": {ServedAt: newer, PromptTokens: 120_000, MessageCount: 50},
	}, got.ModelCaches)
}
