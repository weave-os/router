package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
)

func TestFederationConfigurationKeepsSnowflakeIdentitySeparate(t *testing.T) {
	snowflakePath := filepath.Join(t.TempDir(), "snowflake-jwt")
	require.NoError(t, os.WriteFile(snowflakePath, []byte("snowflake.identity.signature"), 0600))
	t.Setenv("ROUTER_WIF_PROVIDER", auth.WIFProviderOIDC)
	t.Setenv("ROUTER_WIF_OIDC_TOKEN_FILE", snowflakePath)
	t.Setenv("ROUTER_ANTHROPIC_WIF_PROVIDER", "")
	assert.Nil(t, buildAnthropicWIFTokenSource(), "Snowflake configuration cannot implicitly enable Claude federation")

	t.Setenv("ROUTER_ANTHROPIC_WIF_PROVIDER", auth.WIFProviderOIDC)
	t.Setenv("ROUTER_ANTHROPIC_WIF_OIDC_TOKEN_FILE", "/separate/anthropic/identity")
	require.NotNil(t, buildAnthropicWIFTokenSource())
	source := buildWIFTokenSource(slog.New(slog.NewTextHandler(io.Discard, nil)))
	token, err := source.Attestation(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "WIF.OIDC.snowflake.identity.signature", string(token))

	t.Setenv("ROUTER_WIF_PROVIDER", "")
	assert.Nil(t, buildWIFTokenSource(slog.New(slog.NewTextHandler(io.Discard, nil))), "Claude configuration cannot implicitly enable Snowflake federation")
}

func TestAnthropicFederationRejectsIncompleteDeploymentConfiguration(t *testing.T) {
	t.Setenv("ROUTER_ANTHROPIC_WIF_PROVIDER", auth.WIFProviderOIDC)
	t.Setenv("ROUTER_ANTHROPIC_WIF_OIDC_TOKEN_FILE", "")
	assert.Panics(t, func() { buildAnthropicWIFTokenSource() })
	t.Setenv("ROUTER_ANTHROPIC_WIF_PROVIDER", "unsupported")
	assert.Panics(t, func() { buildAnthropicWIFTokenSource() })
}
