package catalog_test

import (
	"math"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
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
	got := catalog.EffectiveInputCost(100, 10, 0, 20, price, "openai")
	assert.InDelta(t, 0.0002, got, 1e-12)

	price.CacheWriteMultiplier = 0
	legacy := catalog.EffectiveInputCost(100, 10, 0, 20, price, "openai")
	assert.InDelta(t, 0.000185, legacy, 1e-12, "unspecified values preserve legacy 1.25x behavior")
}

// The 1-hour TTL tier (issue #867): Anthropic charges 2x base input for 1h
// cache writes, not the 5-minute 1.25x the aggregate-only math applied.
func TestEffectiveInputCost_OneHourCacheWrite(t *testing.T) {
	// claude-opus-5's published rate, as in the issue's worked example.
	opus := catalog.Pricing{InputUSDPer1M: 5, CacheReadMultiplier: 0.10}

	// All 1M creation tokens on the 1h tier, 10 fresh: (10 + 1M*2) * 5/1M
	// = $10.00005 — the issue's expected number, vs $6.25005 before.
	got := catalog.EffectiveInputCost(10, 1_000_000, 1_000_000, 0, opus, providers.ProviderAnthropic)
	assert.Equal(t, int64(10_000_050), catalog.USDToMicros(got))

	// No TTL breakdown (Bedrock/Vertex aggregate-only): byte-identical legacy
	// behavior — every write at 1.25x.
	legacy := catalog.EffectiveInputCost(10, 1_000_000, 0, 0, opus, providers.ProviderAnthropic)
	assert.Equal(t, int64(6_250_050), catalog.USDToMicros(legacy))

	// Mixed tiers, as a real Claude Code session reports them.
	mixed := catalog.EffectiveInputCost(2, 248, 100, 0, opus, providers.ProviderAnthropic)
	wantMixed := (float64(2) + 148*1.25 + 100*2.0) / 1_000_000 * 5
	assert.InDelta(t, wantMixed, mixed, 1e-15)

	// Anthropic's documented web_search anomaly: aggregate above the split's
	// sum — the unattributed remainder prices at the 5-minute rate, never 2x.
	remainder := catalog.EffectiveInputCost(0, 1000, 100, 0, opus, providers.ProviderAnthropic)
	wantRemainder := (900*1.25 + 100*2.0) / 1_000_000 * 5
	assert.InDelta(t, wantRemainder, remainder, 1e-15)

	// Inconsistent payload (1h above the aggregate) must not under-price via
	// a negative 5-minute term; the 1h count clamps to the aggregate.
	clamped := catalog.EffectiveInputCost(0, 80, 100, 0, opus, providers.ProviderAnthropic)
	wantClamped := 80 * 2.0 / 1_000_000 * 5
	assert.InDelta(t, wantClamped, clamped, 1e-15)

	// A binding may carry its own 1h rate; zero inherits the default 2x.
	custom := catalog.Pricing{InputUSDPer1M: 5, CacheWriteMultiplier1h: 3}
	customGot := catalog.EffectiveInputCost(0, 100, 100, 0, custom, providers.ProviderAnthropic)
	assert.InDelta(t, 100*3.0/1_000_000*5, customGot, 1e-15)
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

	shortInput := catalog.EffectiveInputCost(100_000, 10_000, 0, 80_000, price, "openai")
	shortOutput := catalog.EffectiveOutputCost(100_000, 1_000, price)
	assert.InDelta(t, 0.0061, shortInput, 1e-12)
	assert.InDelta(t, 0.0012, shortOutput, 1e-12)

	longInput := catalog.EffectiveInputCost(300_000, 200_000, 0, 90_000, price, "openai")
	longOutput := catalog.EffectiveOutputCost(300_000, 1_000, price)
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
