package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

func TestUtilityTarget_PrefersDefaultWhenServable(t *testing.T) {
	provider, model, ok := policy.UtilityTarget(policy.UtilityTargetRequest{
		EnabledProviders: set(providers.ProviderAnthropic, providers.ProviderOpenAI),
	})
	require.True(t, ok)
	assert.Equal(t, policy.UtilityHardPinDefaultProvider, provider)
	assert.Equal(t, policy.UtilityHardPinDefaultModel, model)
}

func TestUtilityTarget_ExcludedDefaultFallsToCheapestLowTier(t *testing.T) {
	provider, model, ok := policy.UtilityTarget(policy.UtilityTargetRequest{
		EnabledProviders: set(providers.ProviderAnthropic),
		ExcludedModels:   set(policy.UtilityHardPinDefaultModel),
	})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAnthropic, provider)
	assert.NotEqual(t, policy.UtilityHardPinDefaultModel, model)
	assert.Equal(t, catalog.TierLow, catalog.TierFor(model))
}

func TestUtilityTarget_DefaultUnreachableUsesEnabledProvider(t *testing.T) {
	provider, model, ok := policy.UtilityTarget(policy.UtilityTargetRequest{
		EnabledProviders: set(providers.ProviderOpenAI),
	})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderOpenAI, provider)
	assert.Equal(t, "gpt-4.1-nano", model, "cheapest low-tier OpenAI binding")
}

// Sub-agent turns run a tool loop, so a cheaper model flagged weak at agentic
// use must lose to a capable one.
func TestUtilityTarget_RanksWeakModelsLast(t *testing.T) {
	_, model, ok := policy.UtilityTarget(policy.UtilityTargetRequest{
		EnabledProviders: set(providers.ProviderGoogle),
	})
	require.True(t, ok)
	m, known := catalog.ByID(model)
	require.True(t, known)
	assert.NotEqual(t, catalog.AgenticLow, m.AgenticUse)
	assert.NotEqual(t, catalog.ToolUseLow, m.ToolUseQuality)
	assert.NotEqual(t, "gemini-3.1-flash-lite-preview", model)
}

func TestUtilityTarget_CustomBindingServesModel(t *testing.T) {
	const endpoint = "self_hosted_endpoint"
	provider, model, ok := policy.UtilityTarget(policy.UtilityTargetRequest{
		EnabledProviders: set(endpoint),
		Bindings:         router.RequestBindings{Custom: map[string][]string{"gpt-4.1-mini": {endpoint}}},
	})
	require.True(t, ok)
	assert.Equal(t, endpoint, provider)
	assert.Equal(t, "gpt-4.1-mini", model)
}

func TestUtilityTarget_GatewayOnlyReachesAliasesOnly(t *testing.T) {
	gateway := providers.ProviderOpenAIGateway
	req := policy.UtilityTargetRequest{
		EnabledProviders: set(gateway, providers.ProviderAnthropic),
		Bindings: router.RequestBindings{
			Custom:   map[string][]string{"gpt-4.1-mini": {gateway}},
			Gateways: set(gateway),
		},
	}
	provider, model, ok := policy.UtilityTarget(req)
	require.True(t, ok)
	assert.Equal(t, gateway, provider)
	assert.Equal(t, "gpt-4.1-mini", model, "the catalog anthropic binding of the default is not reachable through a gateway")

	req.Bindings.Custom["gpt-4.1-mini"] = nil
	_, _, ok = policy.UtilityTarget(req)
	assert.False(t, ok, "a gateway that aliases nothing serves nothing")
}

func TestUtilityTarget_NoServableModel(t *testing.T) {
	cases := map[string]policy.UtilityTargetRequest{
		"no providers": {},
		"every servable model excluded": {
			EnabledProviders: set(providers.ProviderOpenAI),
			ExcludedModels:   excludeAllServedBy(providers.ProviderOpenAI),
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			provider, model, ok := policy.UtilityTarget(req)
			assert.False(t, ok)
			assert.Empty(t, provider)
			assert.Empty(t, model)
		})
	}
}

func excludeAllServedBy(provider string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			if b.Provider == provider {
				out[m.ID] = struct{}{}
			}
		}
	}
	return out
}
