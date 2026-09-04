package catalog

import (
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFastPriceFor_PublishedRatesInheritCacheMultiplier(t *testing.T) {
	fast, ok := FastPriceFor(providers.ProviderOpenAI, "gpt-5.6-luna")
	require.True(t, ok)
	assert.Equal(t, Pricing{InputUSDPer1M: 2.00, OutputUSDPer1M: 12.00, CacheReadMultiplier: 0.10}, fast)

	opusFast, ok := FastPriceFor(providers.ProviderAnthropic, "claude-opus-5")
	require.True(t, ok)
	assert.Equal(t, Pricing{InputUSDPer1M: 10.00, OutputUSDPer1M: 50.00, CacheReadMultiplier: 0.10}, opusFast)
}

func TestFastPriceFor_BaseListPriceUnchanged(t *testing.T) {
	base, ok := PriceFor(providers.ProviderOpenAI, "gpt-5.6-luna")
	require.True(t, ok)
	assert.Equal(t, Pricing{InputUSDPer1M: 1.00, OutputUSDPer1M: 6.00, CacheReadMultiplier: 0.10}, base)
	primary, ok := PrimaryPriceFor("gpt-5.6-luna")
	require.True(t, ok)
	assert.Equal(t, base, primary)
}

func TestFastPriceFor_NoFastTier(t *testing.T) {
	cases := []struct{ name, provider, id string }{
		{"gateway anthropic", providers.ProviderAnthropicGateway, "claude-opus-5"},
		{"gateway openai", providers.ProviderOpenAIGateway, "gpt-5"},
		{"pro has no priority tier", providers.ProviderOpenAI, "gpt-5.4-pro"},
		{"opus 4.7 rejects speed", providers.ProviderAnthropic, "claude-opus-4-7"},
		{"opus 4.6 ignores speed", providers.ProviderAnthropic, "claude-opus-4-6"},
		{"nano has no priority tier", providers.ProviderOpenAI, "gpt-5.4-nano"},
		{"unknown model", providers.ProviderOpenAI, "no-such-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := FastPriceFor(tc.provider, tc.id)
			assert.False(t, ok)
		})
	}
}

func TestSupportsFastMode(t *testing.T) {
	assert.True(t, SupportsFastMode("gpt-5.6-luna"))
	assert.True(t, SupportsFastMode("gpt-5.6-luna-pro"), "alias carries its own fast tier")
	assert.True(t, SupportsFastMode("claude-opus-5"))
	assert.False(t, SupportsFastMode("claude-sonnet-4-6"))
	assert.False(t, SupportsFastMode("gemini-2.5-pro"))
	assert.False(t, SupportsFastMode("unknown"))
}

func TestCatalog_FastPriceOnlyOnFirstPartyBindingsAndAboveList(t *testing.T) {
	for _, m := range Models {
		for _, b := range m.Providers {
			fast, ok := b.FastPricing()
			if !ok {
				continue
			}
			assert.Greater(t, fast.OutputUSDPer1M, b.Price.OutputUSDPer1M, "%s/%s fast output must cost more than list", m.ID, b.Provider)
			assert.Equal(t, b.Price.CacheReadMultiplier, fast.CacheReadMultiplier, "%s/%s cache discount carries over", m.ID, b.Provider)
			assert.Contains(t, []string{providers.ProviderOpenAI, providers.ProviderAnthropic}, b.Provider,
				"%s: fast tier is a first-party knob, not a gateway one", m.ID)
		}
	}
}
