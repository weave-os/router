package catalog

import (
	"slices"
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalog_NoDuplicateIDs(t *testing.T) {
	seen := make(map[string]struct{}, len(Models))
	for _, m := range Models {
		_, dup := seen[m.ID]
		require.False(t, dup, "duplicate model ID %q in catalog", m.ID)
		seen[m.ID] = struct{}{}
	}
}

func TestCatalog_EveryModelHasAtLeastOneBinding(t *testing.T) {
	for _, m := range Models {
		require.NotEmpty(t, m.Providers, "model %q has empty Providers list", m.ID)
	}
}

func TestCatalog_BindingsReferenceCanonicalProviders(t *testing.T) {
	known := map[string]struct{}{
		providers.ProviderAnthropic:        {},
		providers.ProviderOpenAI:           {},
		providers.ProviderGoogle:           {},
		providers.ProviderOpenRouter:       {},
		providers.ProviderFireworks:        {},
		providers.ProviderDeepInfra:        {},
		providers.ProviderBedrock:          {},
		providers.ProviderMakora:           {},
		providers.ProviderMiniMax:          {},
		providers.ProviderTogether:         {},
		providers.ProviderXAI:              {},
		providers.ProviderMeta:             {},
		providers.ProviderWafer:            {},
		providers.ProviderWaferAnthropic:   {},
		providers.ProviderAnthropicGateway: {},
		providers.ProviderOpenAIGateway:    {},
	}
	for _, m := range Models {
		for i, b := range m.Providers {
			_, ok := known[b.Provider]
			require.Truef(t, ok, "model %q binding %d uses unknown provider %q", m.ID, i, b.Provider)
		}
	}
}

func TestCatalog_CodexSubscriptionModelsUseOpenAI(t *testing.T) {
	for _, model := range Models {
		if !model.CodexSubscription {
			continue
		}
		require.NotEmpty(t, model.Providers, "Codex subscription model %q has no provider", model.ID)
		assert.Equal(t, providers.ProviderOpenAI, model.Providers[0].Provider,
			"Codex subscription model %q must resolve through native OpenAI", model.ID)
	}
}

func TestCatalog_BindingsHavePositivePrice(t *testing.T) {
	for _, m := range Models {
		for i, b := range m.Providers {
			assert.Greaterf(t, b.Price.InputUSDPer1M, 0.0, "%s binding %d (%s) has non-positive InputUSDPer1M", m.ID, i, b.Provider)
			assert.Greaterf(t, b.Price.OutputUSDPer1M, 0.0, "%s binding %d (%s) has non-positive OutputUSDPer1M", m.ID, i, b.Provider)
			if b.Price.LongContext != nil {
				assert.Positivef(t, b.Price.LongContext.ThresholdTokens, "%s binding %d (%s) has non-positive long-context threshold", m.ID, i, b.Provider)
				assert.Greaterf(t, b.Price.LongContext.InputUSDPer1M, 0.0, "%s binding %d (%s) has non-positive long-context input price", m.ID, i, b.Provider)
				assert.Greaterf(t, b.Price.LongContext.OutputUSDPer1M, 0.0, "%s binding %d (%s) has non-positive long-context output price", m.ID, i, b.Provider)
				assert.Lessf(t, b.Price.LongContext.ThresholdTokens, ContextWindowFor(m.ID), "%s binding %d (%s) long-context threshold must be below its context window", m.ID, i, b.Provider)
			}
		}
	}
}

func TestByID_DateStrippedFallback(t *testing.T) {
	// claude-opus-4-7-20251001 should resolve to claude-opus-4-7.
	m, ok := ByID("claude-opus-4-7-20251001")
	require.True(t, ok)
	assert.Equal(t, "claude-opus-4-7", m.ID)
}

func TestByID_OpenAIDashedDateStrippedFallback(t *testing.T) {
	// Regression: catalog's stripper previously only handled Anthropic's compact
	// 8-digit suffix and missed OpenAI's dashed YYYY-MM-DD shape.
	m, ok := ByID("gpt-4o-2024-08-06")
	require.True(t, ok)
	assert.Equal(t, "gpt-4o", m.ID)

	p, ok := PriceFor(providers.ProviderOpenAI, "gpt-4o-2024-08-06")
	require.True(t, ok)
	assert.Greater(t, p.InputUSDPer1M, 0.0)
}

func TestByID_UnknownReturnsFalse(t *testing.T) {
	_, ok := ByID("definitely-not-a-model")
	assert.False(t, ok)
}

func TestCodexSubscriptionCoverageComesFromCatalog(t *testing.T) {
	assert.ElementsMatch(t, []string{
		"gpt-5.6-luna",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-6-luna",
		"gpt-6-sol",
		"gpt-6.1-sol",
		"gpt-6.1-terra",
		"gpt-6.2-sol",
	}, CodexSubscriptionModels())
	assert.True(t, CodexSubscriptionCoversModel("gpt-6-luna"))
	assert.False(t, CodexSubscriptionCoversModel("gpt-5.4-nano"))
	assert.False(t, CodexSubscriptionCoversModel("gpt-5.6-luna-pro"))
	assert.False(t, CodexSubscriptionCoversModel("gpt-6-luna-2026-10-01"))
}

func TestPriceFor_UnknownProviderForKnownModel(t *testing.T) {
	// claude-opus-4-7 is anthropic-only — asking for openai must miss.
	_, ok := PriceFor(providers.ProviderOpenAI, "claude-opus-4-7")
	assert.False(t, ok)
}

func TestPriceFor_KnownPair(t *testing.T) {
	p, ok := PriceFor(providers.ProviderAnthropic, "claude-opus-4-7")
	require.True(t, ok)
	assert.InDelta(t, 5.00, p.InputUSDPer1M, 1e-9)
	assert.InDelta(t, 0.10, p.CacheReadMultiplier, 1e-9)
}

func TestPrimaryPriceFor_LegacyOpusIDsResolve(t *testing.T) {
	// claude-opus-4-0/4-1/4-5 are legacy passthrough IDs (finding [30]):
	// registered as capability specs in internal/router/model.go but were
	// missing from catalog.Models, so PrimaryPriceFor silently returned a
	// zero Pricing and billing debited $0 for real usage. Prices per the
	// opus-4-6 comment in catalog.go: 4.1-and-earlier = $15/$75, 4.5 = $5/$25.
	cases := []struct {
		id             string
		inputUSDPer1M  float64
		outputUSDPer1M float64
	}{
		{"claude-opus-4-0", 15.00, 75.00},
		{"claude-opus-4-1", 15.00, 75.00},
		{"claude-opus-4-5", 5.00, 25.00},
	}
	for _, tc := range cases {
		p, ok := PrimaryPriceFor(tc.id)
		require.True(t, ok, "%s must resolve to a catalog entry", tc.id)
		assert.InDelta(t, tc.inputUSDPer1M, p.InputUSDPer1M, 1e-9, "%s input price", tc.id)
		assert.InDelta(t, tc.outputUSDPer1M, p.OutputUSDPer1M, 1e-9, "%s output price", tc.id)
	}
}

func TestResolveBinding_PicksFirstAvailable(t *testing.T) {
	// claude-opus-4-7 is anthropic-only, so it should resolve only when anthropic is available.
	avail := map[string]struct{}{providers.ProviderAnthropic: {}}
	b, ok := ResolveBinding("claude-opus-4-7", avail)
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAnthropic, b.Provider)

	availNoAnthropic := map[string]struct{}{providers.ProviderOpenAI: {}}
	_, ok = ResolveBinding("claude-opus-4-7", availNoAnthropic)
	assert.False(t, ok)
}

func TestTierFor_KnownAndUnknown(t *testing.T) {
	assert.Equal(t, TierHigh, TierFor("claude-opus-4-7"))
	assert.Equal(t, TierLow, TierFor("claude-haiku-4-5"))
	assert.Equal(t, TierLow, TierFor("google/gemma-4-26b-a4b-it"))
	assert.Equal(t, TierUnknown, TierFor("definitely-not-a-model"))
}

func TestResolveBinding_GemmaUsesNativeGoogleUpstreamID(t *testing.T) {
	b, ok := ResolveBinding("google/gemma-4-26b-a4b-it", map[string]struct{}{providers.ProviderGoogle: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderGoogle, b.Provider)
	assert.Equal(t, "gemma-4-26b-a4b-it", b.UpstreamID)
}

func TestResolveBinding_MiniMaxUsesNativeModelIDs(t *testing.T) {
	cases := []struct {
		model                                       string
		input, output                               float64
		longContextInput, longContextOutput         float64
		fastInput, fastOutput                       float64
		fastLongContextInput, fastLongContextOutput float64
	}{
		{"gpt-6.2-sol", 2, 10, 4, 15, 4, 20, 8, 30},
		{"gpt-6.2-astra", 8, 40, 16, 60, 16, 80, 32, 120},
	}
	for _, tc := range cases {
		m, ok := ByID(tc.model)
		require.True(t, ok, tc.model)
		binding := m.Providers[0]
		for _, tier := range []struct {
			price                                       Pricing
			wantInput, wantOutput                       float64
			wantLongContextInput, wantLongContextOutput float64
		}{
			{binding.Price, tc.input, tc.output, tc.longContextInput, tc.longContextOutput},
			{binding.FastPrice, tc.fastInput, tc.fastOutput, tc.fastLongContextInput, tc.fastLongContextOutput},
		} {
			assert.InDelta(t, tier.wantInput, tier.price.InputUSDPer1M, 1e-9, tc.model)
			assert.InDelta(t, tier.wantOutput, tier.price.OutputUSDPer1M, 1e-9, tc.model)
			assert.InDelta(t, 0.05, tier.price.CacheReadMultiplier, 1e-9, tc.model)
			require.NotNil(t, tier.price.LongContext, tc.model)
			assert.InDelta(t, tier.wantLongContextInput, tier.price.LongContext.InputUSDPer1M, 1e-9, tc.model)
			assert.InDelta(t, tier.wantLongContextOutput, tier.price.LongContext.OutputUSDPer1M, 1e-9, tc.model)
			assert.InDelta(t, 0.05, tier.price.LongContext.CacheReadMultiplier, 1e-9, tc.model)
		}
	}

	assert.True(t, SupportsFastMode("gpt-6.2-sol"))
	assert.True(t, SupportsFastMode("gpt-6.2-astra"))
	assert.True(t, CodexSubscriptionCoversModel("gpt-6.2-sol"))
	assert.False(t, CodexSubscriptionCoversModel("gpt-6.2-astra"))
}

func TestValidateDeployed_FlagsMissingAndUntiered(t *testing.T) {
	err := ValidateDeployed([]string{"claude-opus-4-7"})
	require.NoError(t, err)

	err = ValidateDeployed([]string{"definitely-not-a-model"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "definitely-not-a-model")

	// gpt-4o is priced but has no tier (passthrough only); flag it.
	err = ValidateDeployed([]string{"gpt-4o"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gpt-4o")
}

// TestResolveBinding_WaferTrailingBindings pin the trailing-binding order:
// Wafer only resolves when the earlier providers are absent, so a deploy with
// Makora/Together/Fireworks wired never displaces them onto Wafer.
func TestResolveBinding_WaferTrailingBindings(t *testing.T) {
	// glm-5.3-flash: DeepInfra leads; Makora, Together, and Wafer trail it,
	// with Fireworks last.
	b, ok := ResolveBinding("z-ai/glm-5.3-flash", map[string]struct{}{providers.ProviderDeepInfra: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderDeepInfra, b.Provider)
	assert.Equal(t, "zai-org/GLM-5.3-Flash", b.UpstreamID)

	b, ok = ResolveBinding("z-ai/glm-5.3-flash", map[string]struct{}{
		providers.ProviderMakora: {}, providers.ProviderTogether: {}, providers.ProviderFireworks: {},
	})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderMakora, b.Provider)
	assert.Equal(t, "zai-org/GLM-5.3-Flash", b.UpstreamID)

	b, ok = ResolveBinding("z-ai/glm-5.3-flash", map[string]struct{}{providers.ProviderFireworks: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderFireworks, b.Provider)
	assert.Equal(t, "accounts/fireworks/models/glm-5p3-flash", b.UpstreamID)

	b, ok = ResolveBinding("z-ai/glm-5.3-flash", map[string]struct{}{providers.ProviderWafer: {}, providers.ProviderFireworks: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderWafer, b.Provider)

	b, ok = ResolveBinding("z-ai/glm-5.3-flash", map[string]struct{}{providers.ProviderTogether: {}, providers.ProviderWafer: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderTogether, b.Provider)
	assert.Equal(t, "zai-org/GLM-5.3-Flash", b.UpstreamID)

	b, ok = ResolveBinding("z-ai/glm-5.3-flash", map[string]struct{}{providers.ProviderWafer: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderWafer, b.Provider)
	assert.Equal(t, "GLM-5.3-Flash", b.UpstreamID)

	b, ok = ResolveBinding("z-ai/glm-5.3-flash", map[string]struct{}{providers.ProviderWaferAnthropic: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderWaferAnthropic, b.Provider)

	// glm-5.3 (full) is Fireworks-only.
	b, ok = ResolveBinding("z-ai/glm-5.3", map[string]struct{}{providers.ProviderFireworks: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderFireworks, b.Provider)
	assert.Equal(t, "accounts/fireworks/models/glm-5p3", b.UpstreamID)

	// glm-5.2: Together leads; Wafer is retired and no longer resolves.
	b, ok = ResolveBinding("z-ai/glm-5.2", map[string]struct{}{providers.ProviderTogether: {}, providers.ProviderWafer: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderTogether, b.Provider)

	_, ok = ResolveBinding("z-ai/glm-5.2", map[string]struct{}{providers.ProviderWafer: {}})
	assert.False(t, ok)

	_, ok = ResolveBinding("z-ai/glm-5.2", map[string]struct{}{providers.ProviderWaferAnthropic: {}})
	assert.False(t, ok)

	_, ok = ResolveBinding("z-ai/glm-5.2", map[string]struct{}{providers.ProviderWafer: {}, providers.ProviderWaferAnthropic: {}})
	assert.False(t, ok)

	// kimi-k3.1: Fireworks leads, Together second, OpenRouter last.
	b, ok = ResolveBinding("moonshotai/kimi-k3.1", map[string]struct{}{providers.ProviderFireworks: {}, providers.ProviderTogether: {}, providers.ProviderOpenRouter: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderFireworks, b.Provider)
	assert.Equal(t, "accounts/fireworks/models/kimi-k3p1-pro", b.UpstreamID)

	b, ok = ResolveBinding("moonshotai/kimi-k3.1", map[string]struct{}{providers.ProviderTogether: {}, providers.ProviderOpenRouter: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderTogether, b.Provider)
	assert.Equal(t, "moonshotai/Kimi-K3.1-Pro", b.UpstreamID)

	b, ok = ResolveBinding("moonshotai/kimi-k3.1", map[string]struct{}{providers.ProviderOpenRouter: {}})
	require.True(t, ok)
	assert.Equal(t, "moonshotai/kimi-k3.1-pro", UpstreamIDFor("moonshotai/kimi-k3.1", b.UpstreamID))

	// kimi-k3: Fireworks leads, OpenRouter second, Wafer last.
	b, ok = ResolveBinding("moonshotai/kimi-k3", map[string]struct{}{providers.ProviderOpenRouter: {}, providers.ProviderWafer: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderOpenRouter, b.Provider)

	b, ok = ResolveBinding("moonshotai/kimi-k3", map[string]struct{}{providers.ProviderWafer: {}})
	require.True(t, ok)
	assert.Equal(t, "Kimi-K3", b.UpstreamID)

	b, ok = ResolveBinding("moonshotai/kimi-k3", map[string]struct{}{providers.ProviderWaferAnthropic: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderWaferAnthropic, b.Provider)
	assert.Equal(t, "Kimi-K3", b.UpstreamID)

	// deepseek-v4-flash: Makora leads, OpenRouter second, Wafer's fast-tier
	// binding last; wafer_anthropic trails it.
	b, ok = ResolveBinding("deepseek/deepseek-v4-flash", map[string]struct{}{providers.ProviderOpenRouter: {}, providers.ProviderWafer: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderOpenRouter, b.Provider)

	b, ok = ResolveBinding("deepseek/deepseek-v4-flash", map[string]struct{}{providers.ProviderWafer: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderWafer, b.Provider)
	assert.Equal(t, "DeepSeek-V4-Flash-0731-Fast", b.UpstreamID)

	b, ok = ResolveBinding("deepseek/deepseek-v4-flash", map[string]struct{}{providers.ProviderWaferAnthropic: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderWaferAnthropic, b.Provider)
	assert.Equal(t, "DeepSeek-V4-Flash-0731-Fast", b.UpstreamID)
}

func TestGLM53FlashProviderPricing(t *testing.T) {
	for _, provider := range []string{providers.ProviderDeepInfra, providers.ProviderMakora, providers.ProviderFireworks} {
		t.Run(provider, func(t *testing.T) {
			p, ok := PriceFor(provider, "z-ai/glm-5.3-flash")
			require.True(t, ok)
			assert.InDelta(t, 0.150, p.InputUSDPer1M, 1e-9)
			assert.InDelta(t, 0.500, p.OutputUSDPer1M, 1e-9)
			assert.InDelta(t, 0.03/0.150, p.CacheReadMultiplier, 1e-9)
		})
	}
}

func TestMaxCandidateProviders(t *testing.T) {
	for _, model := range []string{
		"inclusionai/ling-3.0-flash",
		"xiaomi/mimo-v2.6-flash",
		"xiaomi/mimo-v2.6-pro",
	} {
		binding, ok := ResolveBinding(model, map[string]struct{}{providers.ProviderDeepInfra: {}})
		require.True(t, ok, "DeepInfra should resolve %s", model)
		assert.Equal(t, providers.ProviderDeepInfra, binding.Provider)
	}

	deepSeekBinding, ok := ResolveBinding("deepseek/deepseek-v4.1-flash", map[string]struct{}{
		providers.ProviderDeepInfra: {}, providers.ProviderFireworks: {},
	})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderFireworks, deepSeekBinding.Provider,
		"DeepSeek V4.1 Flash remains Fireworks-primary")
}

func TestProviderBindingAcceptsImageCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  int
		n    int
		want bool
	}{
		{name: "unknown cap", cap: 0, n: 17, want: true},
		{name: "below cap", cap: 4, n: 3, want: true},
		{name: "at cap", cap: 4, n: 4, want: true},
		{name: "above cap", cap: 4, n: 5, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, (ProviderBinding{MaxImagesPerRequest: tc.cap}).AcceptsImageCount(tc.n))
		})
	}
}

func TestAcceptsImageCount(t *testing.T) {
	mimo := "xiaomi/mimo-v2.6-pro"
	tests := []struct {
		name      string
		model     string
		count     int
		providers map[string]struct{}
		want      bool
	}{
		{name: "zero images", model: mimo, count: 0, providers: map[string]struct{}{providers.ProviderDeepInfra: {}}, want: true},
		{name: "within deepinfra cap", model: mimo, count: 4, providers: map[string]struct{}{providers.ProviderDeepInfra: {}}, want: true},
		{name: "over cap with all bindings", model: mimo, count: 17, want: true},
		{name: "over cap with both bindings", model: mimo, count: 17, providers: map[string]struct{}{providers.ProviderDeepInfra: {}, providers.ProviderOpenRouter: {}}, want: true},
		{name: "over cap with deepinfra only", model: mimo, count: 17, providers: map[string]struct{}{providers.ProviderDeepInfra: {}}, want: false},
		{name: "no enabled catalog binding", model: mimo, count: 17, providers: map[string]struct{}{providers.ProviderAnthropic: {}}, want: true},
		{name: "text-only model", model: "qwen/qwen3-235b-a22b-2507", count: 1, want: false},
		{name: "unknown model", model: "unknown/model", count: 17, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AcceptsImageCount(tc.model, tc.count, tc.providers))
		})
	}
}

func TestMiMoV26ProDeepInfraImageLimit(t *testing.T) {
	binding, ok := BindingFor("xiaomi/mimo-v2.6-pro", providers.ProviderDeepInfra)
	require.True(t, ok)
	assert.Equal(t, 4, binding.MaxImagesPerRequest)
}

// TestWaferPricing pins the per-1M rates and cache multipliers as published on
// wafer.ai, since billing debits flow straight through these numbers.
func TestWaferPricing(t *testing.T) {
	cases := []struct {
		model     string
		inputUSD  float64
		outputUSD float64
		cacheRead float64
	}{
		{"z-ai/glm-5.3-flash", 0.150, 0.500, 0.03 / 0.150},
		{"moonshotai/kimi-k3", 3.000, 15.000, 0.10},
		{"deepseek/deepseek-v4-flash", 0.280, 0.560, 0.07 / 0.280},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			for _, provider := range []string{providers.ProviderWafer, providers.ProviderWaferAnthropic} {
				p, ok := PriceFor(provider, tc.model)
				require.True(t, ok)
				assert.InDelta(t, tc.inputUSD, p.InputUSDPer1M, 1e-9)
				assert.InDelta(t, tc.outputUSD, p.OutputUSDPer1M, 1e-9)
				assert.InDelta(t, tc.cacheRead, p.CacheReadMultiplier, 1e-9)
			}
		})
	}
}

func TestAnthropicGatewayPricesCacheReadsLikeDirectAnthropic(t *testing.T) {
	for _, model := range []string{"claude-opus-5", "claude-sonnet-5", "claude-opus-4-8"} {
		t.Run(model, func(t *testing.T) {
			gateway, ok := PriceFor(providers.ProviderAnthropicGateway, model)
			require.True(t, ok)
			direct, ok := PriceFor(providers.ProviderAnthropic, model)
			require.True(t, ok)

			assert.InDelta(t, 0.10, gateway.CacheReadMultiplier, 1e-9)
			assert.InDelta(t,
				EffectiveInputCost(0, 0, 1_000_000, direct, providers.ProviderAnthropic, UsageModifiers{}),
				EffectiveInputCost(0, 0, 1_000_000, gateway, providers.ProviderAnthropicGateway, UsageModifiers{}),
				1e-9,
			)
		})
	}
}

func TestSonnet55AnthropicCacheReadMultiplier(t *testing.T) {
	for _, provider := range []string{providers.ProviderAnthropic, providers.ProviderAnthropicGateway} {
		t.Run(provider, func(t *testing.T) {
			price, ok := PriceFor(provider, "claude-sonnet-5-5")
			require.True(t, ok)
			assert.InDelta(t, 0.05, price.EffectiveCacheReadMultiplier(), 1e-9)
		})
	}
}

func TestSubscriptionOverflowBindingsStayOutOfRouting(t *testing.T) {
	openRouterOnly := map[string]struct{}{providers.ProviderOpenRouter: {}}
	_, routable := RoutingTargetSet(openRouterOnly)["claude-opus-4-7"]
	assert.False(t, routable, "an overflow binding must not make a model routable")
	_, ok := ResolveBinding("claude-opus-4-7", openRouterOnly)
	assert.False(t, ok)
	assert.Empty(t, EnumerateBindings("gpt-5.4", openRouterOnly))
	assert.Equal(t, providers.ProviderOpenRouter, CustomProviderFor("claude-opus-4-7", openRouterOnly, map[string][]string{"claude-opus-4-7": {providers.ProviderOpenRouter}}),
		"a BYOK alias must still bind where only an overflow binding exists")

	binding, ok := BindingFor("claude-opus-4-7", providers.ProviderOpenRouter)
	require.True(t, ok)
	assert.True(t, binding.SubscriptionOverflowOnly)
	assert.Equal(t, "anthropic/claude-opus-4.7", binding.UpstreamID)
	price, ok := PriceFor(providers.ProviderOpenRouter, "claude-opus-4-7")
	require.True(t, ok)
	assert.Equal(t, binding.Price, price)
}

func TestSubscriptionOverflowBindingsTrailOpenRouterVendorModels(t *testing.T) {
	for _, model := range Models {
		for index, binding := range model.Providers {
			if !binding.SubscriptionOverflowOnly {
				continue
			}
			assert.Equal(t, providers.ProviderOpenRouter, binding.Provider, model.ID)
			assert.Equal(t, len(model.Providers)-1, index, "%s: overflow binding must trail every routable binding", model.ID)
			assert.NotEmpty(t, binding.UpstreamID, model.ID)
		}
	}
}

func TestCustomToolUnsupportedSet(t *testing.T) {
	// MiMo v2.6 Pro returns {} for Codex's projected exec tool on DeepInfra and
	// OpenRouter alike, so the flag is model-wide. Its Flash sibling is unaffected.
	set := CustomToolUnsupportedSet()
	assert.Contains(t, set, ModelMiMoV26Pro)
	assert.NotContains(t, set, ModelMiMoV26Flash)
	assert.NotContains(t, set, ModelIDClaudeOpus47.String())
}

func TestTier_OrderingIsAscendingByCapability(t *testing.T) {
	assert.Less(t, TierUnknown, TierLow)
	assert.Less(t, TierLow, TierMid, "the planner upgrades a pin when freshTier > pinTier")
	assert.Less(t, TierMid, TierHigh)
}

func TestPricing_ZeroCacheMultipliersFallBackToDefaults(t *testing.T) {
	var unset Pricing
	assert.InDelta(t, 0.5, unset.EffectiveCacheReadMultiplier(), 1e-12)
	assert.InDelta(t, 1.25, unset.EffectiveCacheWriteMultiplier(), 1e-12)

	set := Pricing{CacheReadMultiplier: 0.1, CacheWriteMultiplier: 2}
	assert.InDelta(t, 0.1, set.EffectiveCacheReadMultiplier(), 1e-12)
	assert.InDelta(t, 2.0, set.EffectiveCacheWriteMultiplier(), 1e-12)
}

func TestClassifierRescueOrder_StrongestFirstAndSkipsForceOnly(t *testing.T) {
	got := ClassifierRescueOrder([]string{"claude-haiku-5-5", "claude-fable-5-2", "claude-opus-5-5", "not-a-model", "claude-sonnet-5-5"})
	if want := []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}; !slices.Equal(got, want) {
		t.Fatalf("ClassifierRescueOrder = %v, want %v", got, want)
	}
}
