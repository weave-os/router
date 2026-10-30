package main

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/providers"
)

func TestFederationConfigurationRoundTrip(t *testing.T) {
	dsn, err := fixtureDSN(os.Getenv(databaseURLEnv))
	if err != nil {
		t.Skipf("%s not set to a loopback fixture: %v", databaseURLEnv, err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	repo := postgres.NewRepository(tx, auth.NoOpEncryptor{})
	installation, err := repo.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: uuid.NewString(), Name: "Federation fixture"})
	require.NoError(t, err)
	config := &auth.AnthropicFederationConfig{FederationRuleID: "fdrl_synthetic", OrganizationID: uuid.NewString(), ServiceAccountID: "svac_synthetic", Audience: auth.AnthropicFederationAudience}
	saved, err := repo.ExternalAPIKeys.Create(ctx, auth.CreateExternalAPIKeyParams{InstallationID: installation.ID, ExternalID: uuid.NewString(), Provider: providers.ProviderAnthropic, KeyCiphertext: []byte{}, AuthType: auth.AuthTypeAnthropicWIF, FederationConfig: config})
	require.NoError(t, err)
	assert.Equal(t, config, saved.FederationConfig)
	keys, err := repo.ExternalAPIKeys.GetForInstallation(ctx, installation.ID)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, config, keys[0].FederationConfig)
	assert.Empty(t, keys[0].Plaintext)
	updated, err := repo.ExternalAPIKeys.UpdateModelAliases(ctx, installation.ID, saved.ID, map[string]string{"claude-opus-5-5": "claude-opus-5-5"})
	require.NoError(t, err)
	assert.Equal(t, config, updated.FederationConfig)
	foreign, err := repo.ExternalAPIKeys.GetForInstallation(ctx, uuid.NewString())
	require.NoError(t, err)
	assert.Empty(t, foreign)
}
