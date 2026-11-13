package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
)

func TestMiMoV26FlashProviderOrder(t *testing.T) {
	cases := []struct {
		name       string
		available  map[string]struct{}
		provider   string
		upstreamID string
	}{
		{"all providers", map[string]struct{}{providers.ProviderMakora: {}, providers.ProviderDeepInfra: {}, providers.ProviderOpenRouter: {}}, providers.ProviderMakora, "XiaomiMiMo/MiMo-V2.6-Flash-RL"},
		{"Makora only", map[string]struct{}{providers.ProviderMakora: {}}, providers.ProviderMakora, "XiaomiMiMo/MiMo-V2.6-Flash-RL"},
		{"DeepInfra fallback", map[string]struct{}{providers.ProviderDeepInfra: {}, providers.ProviderOpenRouter: {}}, providers.ProviderDeepInfra, "XiaomiMiMo/MiMo-V2.6-Flash"},
		{"OpenRouter fallback", map[string]struct{}{providers.ProviderOpenRouter: {}}, providers.ProviderOpenRouter, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding, ok := ResolveBinding(ModelMiMoV26Flash, tc.available)
			require.True(t, ok)
			require.Equal(t, tc.provider, binding.Provider)
			require.Equal(t, tc.upstreamID, binding.UpstreamID)
		})
	}
}

func TestMiMoV26FlashProviderPricing(t *testing.T) {
	cases := []struct {
		provider string
		inputUSD float64
		cacheUSD float64
	}{
		{providers.ProviderMakora, 0.13, 0.002},
		{providers.ProviderDeepInfra, 0.14, 0.0028},
		{providers.ProviderOpenRouter, 0.14, 0.014},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			price, ok := PriceFor(tc.provider, ModelMiMoV26Flash)
			require.True(t, ok)
			require.Equal(t, tc.inputUSD, price.InputUSDPer1M)
			require.Equal(t, 0.28, price.OutputUSDPer1M)
			require.InDelta(t, tc.cacheUSD, EffectiveInputCost(1_000_000, 0, 1_000_000, price, tc.provider, UsageModifiers{}), 1e-12)
		})
	}
	primaryPrice, ok := PrimaryPriceFor(ModelMiMoV26Flash)
	require.True(t, ok)
	require.Equal(t, 0.13, primaryPrice.InputUSDPer1M)
	require.Equal(t, TierMid, TierFor(ModelMiMoV26Flash))
	require.Equal(t, 1_048_576, ContextWindowFor(ModelMiMoV26Flash))
}
