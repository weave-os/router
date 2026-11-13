package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
)

const deepSeekV4_1Flash = "deepseek/deepseek-v4.1-flash"

func TestDeepSeekV4_1FlashMakoraBinding(t *testing.T) {
	binding, ok := ResolveBinding(deepSeekV4_1Flash, map[string]struct{}{
		providers.ProviderMakora: {}, providers.ProviderFireworks: {}, providers.ProviderOpenRouter: {},
	})
	require.True(t, ok)
	require.Equal(t, providers.ProviderMakora, binding.Provider)
	require.Equal(t, "deepseek-ai/DeepSeek-V4.1-Flash", binding.UpstreamID)
	require.Equal(t, 0.20, binding.Price.InputUSDPer1M)
	require.Equal(t, 0.99, binding.Price.OutputUSDPer1M)
	require.InDelta(t, 0.006, binding.Price.InputUSDPer1M*binding.Price.CacheReadMultiplier, 1e-12)
	require.Equal(t, 1_048_576, ContextWindowFor(deepSeekV4_1Flash))
}

func TestRetiredDeepSeekFlashDoesNotResolveMakora(t *testing.T) {
	_, ok := ResolveBinding("deepseek/deepseek-v4-flash", map[string]struct{}{providers.ProviderMakora: {}})
	require.False(t, ok)
	require.Equal(t, TierUnknown, TierFor("deepseek/deepseek-v4-flash"))
}

func TestDeepSeekV4_1FlashKeepsFallbackBindings(t *testing.T) {
	for _, provider := range []string{providers.ProviderFireworks, providers.ProviderOpenRouter} {
		t.Run(provider, func(t *testing.T) {
			binding, ok := ResolveBinding(deepSeekV4_1Flash, map[string]struct{}{provider: {}})
			require.True(t, ok)
			require.Equal(t, provider, binding.Provider)
		})
	}
}
