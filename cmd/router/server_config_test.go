package main

import (
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("ROUTER_COOKIE_INSECURE", "")
	t.Setenv("ROUTER_STICKY_DECISION_TTL_MS", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropic), "")

	cfg := adminConfigFromEnv()
	assert.False(t, cfg.CookieInsecure)
	assert.Equal(t, "0", cfg.StickyDecisionTTL)
	assert.False(t, cfg.OtelEnabled)
	assert.False(t, cfg.HasDeploymentProviderKey(providers.ProviderAnthropic))
}

func TestAdminConfigFromEnvParsing(t *testing.T) {
	t.Setenv("ROUTER_STICKY_DECISION_TTL_MS", "1500")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector:4318")
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropic), "synthetic-key")
	for value, insecure := range map[string]bool{"true": true, "TRUE": false, "1": false, " true": false} {
		t.Setenv("ROUTER_COOKIE_INSECURE", value)
		assert.Equal(t, insecure, adminConfigFromEnv().CookieInsecure, "ROUTER_COOKIE_INSECURE=%q", value)
	}

	cfg := adminConfigFromEnv()
	assert.Equal(t, "1500", cfg.StickyDecisionTTL)
	assert.True(t, cfg.OtelEnabled)
	assert.True(t, cfg.HasDeploymentProviderKey(providers.ProviderAnthropic))
}

func TestInternalServiceTokenFromEnvTrims(t *testing.T) {
	t.Setenv("ROUTER_INTERNAL_SERVICE_TOKEN", "  synthetic-token \n")
	assert.Equal(t, "synthetic-token", internalServiceTokenFromEnv())
	t.Setenv("ROUTER_INTERNAL_SERVICE_TOKEN", "   ")
	assert.Empty(t, internalServiceTokenFromEnv())
}

func TestDefaultStrategyFromEnv(t *testing.T) {
	t.Setenv("ROUTER_DEFAULT_STRATEGY", "")
	strategy, err := defaultStrategyFromEnv()
	require.NoError(t, err)
	assert.Equal(t, router.StrategyHMMEmbedding, strategy)

	t.Setenv("ROUTER_DEFAULT_STRATEGY", string(router.StrategyLLMClassifier))
	_, err = defaultStrategyFromEnv()
	require.EqualError(t, err, `ROUTER_DEFAULT_STRATEGY="`+string(router.StrategyLLMClassifier)+`" is not a valid deployment default: beta and classifier lanes are opt-in per installation`)
}

func TestMiddlewareConfigFromEnv(t *testing.T) {
	t.Setenv("ROUTER_INFERENCE_ORGANIZATION_ALLOWLIST", "")
	assert.Nil(t, middlewareConfigFromEnv().InferenceOrganizationAllowlist)
	t.Setenv("ROUTER_INFERENCE_ORGANIZATION_ALLOWLIST", " org-a , ,org-b")
	assert.Equal(t, []string{"org-a", "org-b"}, middlewareConfigFromEnv().InferenceOrganizationAllowlist)
}
