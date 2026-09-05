package catalog_test

import (
	"math"
	"testing"

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
	got := catalog.EffectiveInputCost(100, 10, 20, price, "openai")
	assert.InDelta(t, 0.0002, got, 1e-12)

	price.CacheWriteMultiplier = 0
	legacy := catalog.EffectiveInputCost(100, 10, 20, price, "openai")
	assert.InDelta(t, 0.000185, legacy, 1e-12, "unspecified values preserve legacy 1.25x behavior")
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

	shortInput := catalog.EffectiveInputCost(100_000, 10_000, 80_000, price, "openai")
	shortOutput := catalog.EffectiveOutputCost(100_000, 1_000, price)
	assert.InDelta(t, 0.0061, shortInput, 1e-12)
	assert.InDelta(t, 0.0012, shortOutput, 1e-12)

	longInput := catalog.EffectiveInputCost(300_000, 200_000, 90_000, price, "openai")
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
