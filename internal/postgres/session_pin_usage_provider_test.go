package postgres_test

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/postgres"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionPinRepo_UpdateUsageProviderTracksPinnedModel(t *testing.T) {
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

	repo := postgres.NewSessionPinRepo(pool)
	tests := []struct {
		name           string
		pinnedModel    string
		pinnedProvider string
		servedModel    string
		servedProvider string
		wantProvider   string
		wantModel      string
	}{
		{
			name:        "cross-model rescue preserves pinned provider",
			pinnedModel: "claude-opus-4-8", pinnedProvider: providers.ProviderAnthropic,
			servedModel: "deepseek/deepseek-v4-pro", servedProvider: providers.ProviderFireworks,
			wantProvider: providers.ProviderAnthropic, wantModel: "claude-opus-4-8",
		},
		{
			name:        "same model binding failover updates provider with effort",
			pinnedModel: "claude-opus-4-8", pinnedProvider: providers.ProviderAnthropic,
			servedModel: "claude-opus-4-8:high", servedProvider: providers.ProviderBedrock,
			wantProvider: providers.ProviderBedrock, wantModel: "claude-opus-4-8",
		},
		{
			name:        "empty pinned model follows served provider",
			pinnedModel: "", pinnedProvider: providers.ProviderAnthropic,
			servedModel: "deepseek/deepseek-v4-pro", servedProvider: providers.ProviderFireworks,
			wantProvider: providers.ProviderFireworks, wantModel: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			var key [sessionpin.SessionKeyLen]byte
			copy(key[:], id[:sessionpin.SessionKeyLen])
			pin := sessionpin.Pin{
				SessionKey: key, Role: sessionpin.DefaultRole, InstallationID: installationID,
				Provider: tc.pinnedProvider, Model: tc.pinnedModel, Reason: "test",
				Strategy: router.StrategyCluster, TurnCount: 1, PinnedUntil: time.Now().Add(time.Hour),
			}
			require.NoError(t, repo.Upsert(ctx, pin))
			require.NoError(t, repo.UpdateUsage(ctx, key, pin.Role, sessionpin.Usage{
				Strategy: router.StrategyCluster, EndedAt: time.Now().UTC(),
				ServedModel: tc.servedModel, ServedProvider: tc.servedProvider,
			}))

			got, found, err := repo.Get(ctx, key, pin.Role)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, tc.wantProvider, got.Provider)
			assert.Equal(t, tc.wantModel, got.Model)
			assert.Equal(t, tc.servedModel, got.LastServedModel)
		})
	}
}
