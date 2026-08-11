package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router/catalog"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// bindingWithUpstreamID returns the first catalog model bound to provider with a
// non-empty UpstreamID, so tests survive catalog churn instead of pinning an ID.
func bindingWithUpstreamID(t *testing.T, provider string) (modelID, upstreamID string) {
	t.Helper()
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			if b.Provider == provider && b.UpstreamID != "" {
				return m.ID, b.UpstreamID
			}
		}
	}
	t.Skipf("catalog has no %s binding with an UpstreamID", provider)
	return "", ""
}

func TestModelAliasEnvVar(t *testing.T) {
	assert.Equal(t, "ROUTER_OPENROUTER_MODEL_ALIASES", modelAliasEnvVar(providers.ProviderOpenRouter))
	assert.Equal(t, "ROUTER_BEDROCK_MODEL_ALIASES", modelAliasEnvVar(providers.ProviderBedrock))
}

func TestParseModelAliases(t *testing.T) {
	t.Run("empty value yields no entries", func(t *testing.T) {
		aliases, err := parseModelAliases("   ")
		require.NoError(t, err)
		assert.Empty(t, aliases)
	})

	t.Run("decodes a JSON object", func(t *testing.T) {
		aliases, err := parseModelAliases(`{"deepseek/deepseek-v4-flash":"deepseek-v4-flash","z-ai/glm-5.2":"glm-5.2"}`)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{
			"deepseek/deepseek-v4-flash": "deepseek-v4-flash",
			"z-ai/glm-5.2":               "glm-5.2",
		}, aliases)
	})

	t.Run("rejects malformed JSON", func(t *testing.T) {
		_, err := parseModelAliases(`{"deepseek/deepseek-v4-flash":`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "JSON object")
	})

	t.Run("rejects a JSON array", func(t *testing.T) {
		_, err := parseModelAliases(`["deepseek-v4-flash"]`)
		require.Error(t, err)
	})

	t.Run("rejects an empty upstream model ID", func(t *testing.T) {
		_, err := parseModelAliases(`{"deepseek/deepseek-v4-flash":"  "}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deepseek/deepseek-v4-flash")
	})

	t.Run("rejects an empty model ID", func(t *testing.T) {
		_, err := parseModelAliases(`{"":"deepseek-v4-flash"}`)
		require.Error(t, err)
	})
}

func TestResolveModelAliasesAppliesEnvOverride(t *testing.T) {
	require.NotEmpty(t, catalog.Models)
	modelID := catalog.Models[0].ID
	t.Setenv(modelAliasEnvVar(providers.ProviderOpenRouter), `{"`+modelID+`":"gateway-name"}`)

	aliases, err := resolveModelAliases(discardLogger())
	require.NoError(t, err)
	assert.Equal(t, "gateway-name", aliases[providers.ProviderOpenRouter][modelID])
}

func TestResolveModelAliasesOverridesCatalogUpstreamID(t *testing.T) {
	modelID, catalogUpstreamID := bindingWithUpstreamID(t, providers.ProviderTogether)
	t.Setenv(modelAliasEnvVar(providers.ProviderTogether), `{"`+modelID+`":"operator-name"}`)

	aliases, err := resolveModelAliases(discardLogger())
	require.NoError(t, err)
	assert.Equal(t, "operator-name", aliases[providers.ProviderTogether][modelID])
	assert.NotEqual(t, catalogUpstreamID, aliases[providers.ProviderTogether][modelID])
}

func TestResolveModelAliasesKeepsUnaliasedCatalogBindings(t *testing.T) {
	modelID, catalogUpstreamID := bindingWithUpstreamID(t, providers.ProviderTogether)
	require.NotEmpty(t, catalog.Models)
	t.Setenv(modelAliasEnvVar(providers.ProviderTogether), `{"`+catalog.Models[0].ID+`":"operator-name"}`)

	aliases, err := resolveModelAliases(discardLogger())
	require.NoError(t, err)
	assert.Equal(t, catalogUpstreamID, aliases[providers.ProviderTogether][modelID])
}

func TestResolveModelAliasesSkipsUnknownModel(t *testing.T) {
	t.Setenv(modelAliasEnvVar(providers.ProviderOpenRouter), `{"vendor/not-in-catalog":"whatever"}`)

	aliases, err := resolveModelAliases(discardLogger())
	require.NoError(t, err)
	assert.NotContains(t, aliases[providers.ProviderOpenRouter], "vendor/not-in-catalog")
}

func TestResolveModelAliasesRejectsMalformedValue(t *testing.T) {
	t.Setenv(modelAliasEnvVar(providers.ProviderFireworks), "not-json")

	_, err := resolveModelAliases(discardLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ROUTER_FIREWORKS_MODEL_ALIASES")
}
