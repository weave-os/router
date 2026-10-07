package catalog_test

import (
	"math"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyUsdToMicros is byte-for-byte the pre-consolidation
// internal/postgres/telemetry.go implementation (NaN/Inf guard only, no
// negative guard). Kept here as a golden reference so the shared
// catalog.USDToMicros can be proven to reproduce it exactly.
func legacyUsdToMicros(f float64) int64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int64(math.Round(f * 1_000_000))
}

// legacyComputeNotionalMicros is byte-for-byte the pre-consolidation
// internal/billing/service.go computeNotionalMicros rounding step (NaN/Inf/
// negative guard), applied directly to a precomputed total rather than
// DebitInferenceParams so it can be table-tested against arbitrary floats.
func legacyComputeNotionalMicros(total float64) int64 {
	if math.IsNaN(total) || math.IsInf(total, 0) || total < 0 {
		return 0
	}
	return int64(math.Round(total * 1_000_000))
}

func TestUSDToMicros_MatchesBothLegacyImplementations(t *testing.T) {
	cases := []float64{
		0,
		6.75,
		0.0000005,  // rounds up to 1 micro
		0.00000049, // rounds down to 0 micros
		12.3456785, // exercises round-half behavior on a real fraction-of-cent
		999_999.999999,
		1e-12,
		0.1 + 0.2, // classic float64 imprecision case
	}
	for _, f := range cases {
		got := catalog.USDToMicros(f)
		assert.Equal(t, legacyUsdToMicros(f), got, "diverges from legacy postgres.usdToMicros for %v", f)
		assert.Equal(t, legacyComputeNotionalMicros(f), got, "diverges from legacy billing.computeNotionalMicros for %v", f)
	}
}

func TestUSDToMicros_NaNAndInfCollapseToZero(t *testing.T) {
	assert.Equal(t, int64(0), catalog.USDToMicros(math.NaN()))
	assert.Equal(t, int64(0), catalog.USDToMicros(math.Inf(1)))
	assert.Equal(t, int64(0), catalog.USDToMicros(math.Inf(-1)))
}

func TestUSDToMicros_NegativeCollapsesToZero(t *testing.T) {
	// billing.computeNotionalMicros always guarded negative; postgres.usdToMicros
	// never received a negative input in practice, so extending the guard here
	// is a safe superset, not a behavior change for real traffic.
	assert.Equal(t, int64(0), catalog.USDToMicros(-0.01))
	assert.Equal(t, legacyComputeNotionalMicros(-5), catalog.USDToMicros(-5))
}

func TestUSDToMicros_RoundsHalfAwayFromZero(t *testing.T) {
	// 6.7500005 USD = 6,750,000.5 micros -> rounds to 6,750,001 (math.Round
	// rounds half away from zero, matching both legacy implementations).
	assert.Equal(t, int64(6_750_001), catalog.USDToMicros(6.7500005))
}

func TestEffectiveInputCost_UsesBindingCacheWritePrice(t *testing.T) {
	price := catalog.Pricing{InputUSDPer1M: 2, CacheReadMultiplier: 0.5, CacheWriteMultiplier: 2}
	got := catalog.EffectiveInputCost(100, 10, 20, price, "openai", catalog.UsageModifiers{})
	assert.InDelta(t, 0.0002, got, 1e-12)

	price.CacheWriteMultiplier = 0
	legacy := catalog.EffectiveInputCost(100, 10, 20, price, "openai", catalog.UsageModifiers{})
	assert.InDelta(t, 0.000185, legacy, 1e-12, "unspecified values preserve legacy 1.25x behavior")
}

func TestCounterfactualInputCost_RepricesWarmPrefillAsCacheRead(t *testing.T) {
	opus := catalog.Pricing{InputUSDPer1M: 5, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10}

	// 2k fresh + 100k cold prefill: $0.01 + $0.625 actual; the warm baseline reads the prefill for $0.05.
	assert.InDelta(t, 0.635, catalog.CounterfactualInputCost(2_000, 100_000, 0, 0, opus, "anthropic", catalog.UsageModifiers{}), 1e-12)
	assert.InDelta(t, 0.060, catalog.CounterfactualInputCost(2_000, 100_000, 0, 100_000, opus, "anthropic", catalog.UsageModifiers{}), 1e-12)
}

func TestCounterfactualInputCost_SameResultForEitherUsageShape(t *testing.T) {
	opus := catalog.Pricing{InputUSDPer1M: 5, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10}

	// OpenAI-shaped input_tokens include cached tokens; the fresh split must survive the repricing.
	anthropic := catalog.CounterfactualInputCost(2_000, 100_000, 5_000, 100_000, opus, "anthropic", catalog.UsageModifiers{})
	openai := catalog.CounterfactualInputCost(107_000, 100_000, 5_000, 100_000, opus, "openai", catalog.UsageModifiers{})
	assert.InDelta(t, anthropic, openai, 1e-12)
}

func TestEffectiveInputCost_AnthropicFamilyInputIsFreshOnly(t *testing.T) {
	sonnet := catalog.Pricing{InputUSDPer1M: 3, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10}

	// 2k fresh + 10k write + 100k read: $0.006 + $0.0375 + $0.03.
	for _, provider := range []string{providers.ProviderAnthropic, providers.ProviderAnthropicGateway, providers.ProviderWaferAnthropic} {
		got := catalog.EffectiveInputCost(2_000, 10_000, 100_000, sonnet, provider, catalog.UsageModifiers{})
		assert.InDelta(t, 0.0735, got, 1e-12, provider)
	}
	assert.InDelta(t, 0.0735, catalog.EffectiveInputCost(112_000, 10_000, 100_000, sonnet, providers.ProviderOpenAI, catalog.UsageModifiers{}), 1e-12)
}

func TestCounterfactualInputCost_AnthropicFamilyInputIsFreshOnly(t *testing.T) {
	opus := catalog.Pricing{InputUSDPer1M: 5, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10}

	for _, provider := range []string{providers.ProviderAnthropicGateway, providers.ProviderWaferAnthropic} {
		assert.InDelta(t, 0.060, catalog.CounterfactualInputCost(2_000, 100_000, 0, 100_000, opus, provider, catalog.UsageModifiers{}), 1e-12, provider)
	}
}

func TestEffectiveCost_SelectsLongContextTier(t *testing.T) {
	price := catalog.Pricing{
		InputUSDPer1M:        0.20,
		OutputUSDPer1M:       1.20,
		CacheWriteMultiplier: 1.25,
		CacheReadMultiplier:  0.10,
		LongContext: &catalog.LongContextPricing{
			ThresholdTokens:      272_000,
			InputUSDPer1M:        0.40,
			OutputUSDPer1M:       1.80,
			CacheWriteMultiplier: 1.25,
			CacheReadMultiplier:  0.10,
		},
	}

	shortInput := catalog.EffectiveInputCost(100_000, 10_000, 80_000, price, "openai", catalog.UsageModifiers{})
	shortOutput := catalog.EffectiveOutputCost(100_000, 1_000, price, catalog.UsageModifiers{})
	assert.InDelta(t, 0.0061, shortInput, 1e-12)
	assert.InDelta(t, 0.0012, shortOutput, 1e-12)

	longInput := catalog.EffectiveInputCost(300_000, 200_000, 90_000, price, "openai", catalog.UsageModifiers{})
	longOutput := catalog.EffectiveOutputCost(300_000, 1_000, price, catalog.UsageModifiers{})
	assert.InDelta(t, 0.1076, longInput, 1e-12)
	assert.InDelta(t, 0.0018, longOutput, 1e-12)
}

func TestPricingForInputTokens_ThresholdIsExclusive(t *testing.T) {
	price := catalog.Pricing{
		InputUSDPer1M: 0.20,
		LongContext: &catalog.LongContextPricing{
			ThresholdTokens: 272_000,
			InputUSDPer1M:   0.40,
		},
	}

	assert.Equal(t, 0.20, price.ForInputTokens(272_000).InputUSDPer1M)
	assert.Equal(t, 0.40, price.ForInputTokens(272_001).InputUSDPer1M)
}

func TestSignedUSDToMicrosPreservesNegatives(t *testing.T) {
	if got := catalog.SignedUSDToMicros(-0.012345); got != -12345 {
		t.Fatalf("got %d", got)
	}
	if got := catalog.SignedUSDToMicros(math.NaN()); got != 0 {
		t.Fatalf("NaN got %d", got)
	}
}

func TestEffectiveInputCost_PricesFiveMinuteAndOneHourWritesSeparately(t *testing.T) {
	opus := catalog.Pricing{InputUSDPer1M: 5, CacheReadMultiplier: 0.10}

	// 1k fresh + 40k 5m writes (1.25x) + 60k 1h writes (2x) + 200k reads: $0.005 + $0.25 + $0.60 + $0.10.
	got := catalog.EffectiveInputCost(1_000, 100_000, 200_000, opus, providers.ProviderAnthropic, catalog.UsageModifiers{CacheCreation1h: 60_000})
	assert.InDelta(t, 0.955, got, 1e-12)

	allFiveMinute := catalog.EffectiveInputCost(1_000, 100_000, 200_000, opus, providers.ProviderAnthropic, catalog.UsageModifiers{})
	assert.InDelta(t, 0.730, allFiveMinute, 1e-12, "zero modifiers keep every write at the 5-minute rate")
}

func TestEffectiveInputCost_OneHourShareIsClampedToTotalWrites(t *testing.T) {
	opus := catalog.Pricing{InputUSDPer1M: 5, CacheReadMultiplier: 0.10}

	got := catalog.EffectiveInputCost(0, 10_000, 0, opus, providers.ProviderAnthropic, catalog.UsageModifiers{CacheCreation1h: 50_000})
	assert.InDelta(t, 0.10, got, 1e-12, "10k writes at 2x, never more than the reported total")
}

func TestEffectiveInputCost_OneHourWritesUseFiveMinuteRateOutsideAnthropicFamily(t *testing.T) {
	gpt := catalog.Pricing{InputUSDPer1M: 2, CacheWriteMultiplier: 1.25, CacheReadMultiplier: 0.10}

	withSplit := catalog.EffectiveInputCost(20_000, 10_000, 0, gpt, providers.ProviderOpenAI, catalog.UsageModifiers{CacheCreation1h: 10_000})
	withoutSplit := catalog.EffectiveInputCost(20_000, 10_000, 0, gpt, providers.ProviderOpenAI, catalog.UsageModifiers{})
	assert.InDelta(t, withoutSplit, withSplit, 1e-12)
}

func TestEffectiveCost_USInferenceGeoScalesEveryTokenCategory(t *testing.T) {
	opus, ok := catalog.PriceFor(providers.ProviderAnthropic, "claude-opus-5")
	require.True(t, ok)
	us := catalog.UsageModifiers{CacheCreation1h: 4_000, InferenceGeo: catalog.InferenceGeoUS}
	global := catalog.UsageModifiers{CacheCreation1h: 4_000, InferenceGeo: "global"}

	// 1k fresh + 6k 5m + 4k 1h + 10k reads = $0.005 + $0.0375 + $0.04 + $0.005, then x1.1.
	assert.InDelta(t, 0.0875*1.1, catalog.EffectiveInputCost(1_000, 10_000, 10_000, opus, providers.ProviderAnthropic, us), 1e-12)
	assert.InDelta(t, 0.0875, catalog.EffectiveInputCost(1_000, 10_000, 10_000, opus, providers.ProviderAnthropic, global), 1e-12)
	assert.InDelta(t, 0.025*1.1, catalog.EffectiveOutputCost(1_000, 1_000, opus, us), 1e-12)
	assert.InDelta(t, 0.025, catalog.EffectiveOutputCost(1_000, 1_000, opus, global), 1e-12)
}

func TestEffectiveCost_USInferenceGeoOnlyWhereAnthropicChargesIt(t *testing.T) {
	us := catalog.UsageModifiers{InferenceGeo: catalog.InferenceGeoUS}
	cases := []struct {
		provider string
		model    string
		premium  bool
	}{
		{providers.ProviderAnthropic, "claude-opus-4-6", true},
		{providers.ProviderAnthropic, "claude-sonnet-5-5", true},
		{providers.ProviderAnthropic, "claude-opus-4-5", false},
		{providers.ProviderAnthropic, "claude-haiku-4-5", false},
		{providers.ProviderAnthropicGateway, "claude-opus-5", false},
	}
	for _, tc := range cases {
		price, ok := catalog.PriceFor(tc.provider, tc.model)
		require.True(t, ok, tc.model)
		want := catalog.EffectiveOutputCost(0, 1_000_000, price, catalog.UsageModifiers{})
		if tc.premium {
			want *= catalog.USInferenceGeoMultiplier
		}
		assert.InDelta(t, want, catalog.EffectiveOutputCost(0, 1_000_000, price, us), 1e-12, "%s/%s", tc.provider, tc.model)
	}
}

func TestEffectiveCost_FastRateStacksWithCacheTTLAndGeo(t *testing.T) {
	fast, ok := catalog.FastPriceFor(providers.ProviderAnthropic, "claude-opus-5-5")
	require.True(t, ok)
	m := catalog.UsageModifiers{CacheCreation1h: 10_000, InferenceGeo: catalog.InferenceGeoUS}

	// $8 input: 10k 1h writes at 2x = $0.16, 100k reads at 0.05x = $0.04; $40 output: 1k = $0.04; all x1.1.
	assert.InDelta(t, 0.20*1.1, catalog.EffectiveInputCost(0, 10_000, 100_000, fast, providers.ProviderAnthropic, m), 1e-12)
	assert.InDelta(t, 0.04*1.1, catalog.EffectiveOutputCost(0, 1_000, fast, m), 1e-12)
}

func TestCounterfactualInputCost_ReusesObservedTTLSplit(t *testing.T) {
	opus := catalog.Pricing{InputUSDPer1M: 5, CacheReadMultiplier: 0.10}
	m := catalog.UsageModifiers{CacheCreation1h: 75_000}

	// 100k writes, 75% 1h; half warm: 50k reads ($0.025) + 12.5k 5m ($0.078125) + 37.5k 1h ($0.375).
	got := catalog.CounterfactualInputCost(0, 100_000, 0, 50_000, opus, providers.ProviderAnthropic, m)
	assert.InDelta(t, 0.478125, got, 1e-12)
	// A fully warm baseline writes nothing, so the split contributes nothing.
	assert.InDelta(t, 0.05, catalog.CounterfactualInputCost(0, 100_000, 0, 100_000, opus, providers.ProviderAnthropic, m), 1e-12)
}

func TestSynthesizedBindingsDoNotInheritUSInferenceGeoPremium(t *testing.T) {
	const custom = "custom_anthropic_endpoint"
	available := map[string]struct{}{custom: {}}
	customs := map[string][]string{"claude-fable-5-1": {custom}}

	binding, ok := catalog.ResolveBindingWithCustom("claude-fable-5-1", available, customs)
	require.True(t, ok)
	require.Equal(t, custom, binding.Provider)
	assert.Equal(t, 1.0, binding.Price.EffectiveUSInferenceGeoMultiplier())

	bindings := catalog.EnumerateBindingsWithCustom("claude-fable-5-1", available, customs)
	require.Len(t, bindings, 1)
	assert.Equal(t, 1.0, bindings[0].Price.EffectiveUSInferenceGeoMultiplier())

	primary, ok := catalog.PrimaryPriceFor("claude-fable-5-1")
	require.True(t, ok)
	assert.Equal(t, catalog.USInferenceGeoMultiplier, primary.EffectiveUSInferenceGeoMultiplier(), "first-party binding keeps the premium")
}
