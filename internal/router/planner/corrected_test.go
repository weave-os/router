package planner_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

func correctedCfg(horizon int) planner.EVConfig {
	return planner.EVConfig{
		ThresholdUSD:           0.001,
		ExpectedRemainingTurns: horizon,
		CorrectedEconomics:     true,
	}
}

func legacyCfg(horizon int) planner.EVConfig {
	return planner.EVConfig{ThresholdUSD: 0.001, ExpectedRemainingTurns: horizon}
}

func correctedInputs(pinModel, freshModel string, tokens, prefix, priorOut int, cold bool) planner.Inputs {
	return planner.Inputs{
		Pin: sessionpin.Pin{
			Model:           pinModel,
			Provider:        "anthropic",
			LastTurnEndedAt: time.Now().Add(-time.Minute),
		},
		Fresh:                 router.Decision{Model: freshModel, Provider: "anthropic"},
		EstimatedInputTokens:  tokens,
		CacheablePrefixTokens: prefix,
		CachePrefixKnown:      true,
		PriorOutputTokens:     priorOut,
		PinCacheCold:          cold,
	}
}

// The safety property: merging must not move routing until the flag is armed.
// Sonnet->Haiku is chosen because legacy's break-even (0.25 at N=3, m=0.10)
// sits above Haiku's 0.333x, so legacy stays; Opus->Haiku is 0.20x and legacy
// already switches, so it cannot show the difference.
func TestCorrectedEconomicsIsOffByDefault(t *testing.T) {
	in := correctedInputs("claude-sonnet-5", "claude-haiku-4-5", 200_000, 80_000, 1200, false)

	before := planner.Decide(in, legacyCfg(3))
	after := planner.Decide(in, correctedCfg(3))

	assert.Equal(t, planner.OutcomeStay, before.Outcome,
		"legacy prices the whole prompt at the read multiplier, so a 3x gap fails its 0.25 bar")
	assert.Equal(t, planner.OutcomeSwitch, after.Outcome,
		"corrected economics sees the uncached tail and switches")
}

// The 52-point error, as an assertion. Legacy evaluates every model at
// price*m; a real prompt has a (1-k) tail billed at full price, where the raw
// price gap between two models applies undiscounted.
func TestCorrectedEVRestoresTheUncachedTail(t *testing.T) {
	highK := planner.Decide(
		correctedInputs("claude-opus-5", "claude-haiku-4-5", 200_000, 180_000, 1200, false), correctedCfg(3))
	lowK := planner.Decide(
		correctedInputs("claude-opus-5", "claude-haiku-4-5", 200_000, 80_000, 1200, false), correctedCfg(3))

	assert.Greater(t, lowK.ExpectedSavingsUSD, highK.ExpectedSavingsUSD,
		"a smaller cacheable share leaves more prompt at full price, where the 5x gap bites")
	assert.Less(t, lowK.EvictionCostUSD, highK.EvictionCostUSD,
		"less live cache to destroy makes the switch cheaper")
}

// Cross-validates the Go port against the Python reference in
// router-internal/eval/cache_eviction/policy.py -- the implementation the
// -12.7%..-14.1% replay result was measured with. Values emitted by that module.
func TestCorrectedEVGoldenVectors(t *testing.T) {
	cases := []struct {
		name                   string
		pinIn, pinOut, pinMult float64
		frIn, frOut, frMult    float64
		tokens, k, priorOut    float64
		cold                   bool
		wantGain, wantSwitch   float64
	}{
		{"opus_to_haiku_warm_k09", 5, 25, 0.10, 1, 5, 0.10, 200_000, 0.9, 1200, false, 0.1760000000, 0.2070000000},
		{"opus_to_haiku_warm_k04", 5, 25, 0.10, 1, 5, 0.10, 200_000, 0.4, 1200, false, 0.5360000000, 0.0920000000},
		{"sonnet_to_haiku_warm", 3, 15, 0.10, 1, 5, 0.10, 200_000, 0.9, 1200, false, 0.0880000000, 0.2070000000},
		{"cold_pin_cheaper_fresh", 5, 25, 0.10, 1, 5, 0.10, 200_000, 0.9, 1200, true, 0.8240000000, 0.0},
		{"same_input_dearer_out", 1, 1, 0.10, 1, 50, 0.10, 100_000, 0.9, 10_000, false, -0.4900000000, 0.1035000000},
		{"gateway_default_mult", 3, 15, 0.50, 1, 5, 0.50, 50_000, 0.9, 500, false, 0.0600000000, 0.0337500000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pin := catalog.Pricing{InputUSDPer1M: tc.pinIn, OutputUSDPer1M: tc.pinOut, CacheReadMultiplier: tc.pinMult}
			fresh := catalog.Pricing{InputUSDPer1M: tc.frIn, OutputUSDPer1M: tc.frOut, CacheReadMultiplier: tc.frMult}
			gain, switchCost := planner.CorrectedTermsForTest(pin, fresh, tc.tokens, tc.k, tc.priorOut, tc.cold)
			assert.InDelta(t, tc.wantGain, gain, 1e-9, "per-turn gain must match the Python reference")
			assert.InDelta(t, tc.wantSwitch, switchCost, 1e-9, "switch cost must match the Python reference")
		})
	}
}

// Legacy is blind to output price, so a model many times dearer per output
// token can score EV-positive on input alone.
func TestCorrectedEVCountsOutputPrice(t *testing.T) {
	in := correctedInputs("claude-haiku-4-5", "claude-opus-5", 200_000, 180_000, 60_000, false)
	withOutput := planner.Decide(in, correctedCfg(3))

	noOutput := in
	noOutput.PriorOutputTokens = 0
	require.NotEqual(t, withOutput.ExpectedSavingsUSD,
		planner.Decide(noOutput, correctedCfg(3)).ExpectedSavingsUSD,
		"the output term must move the EV")
	assert.Equal(t, planner.OutcomeStay, withOutput.Outcome,
		"switching to a 5x-dearer-output model on a 60k-token completion is not a saving")
}

// An un-migrated call site — one that supplies no prefix telemetry at all —
// must degrade to the old behaviour, not to a wild k=0.
func TestCacheableShareFallsBackToLegacyWhenUninstrumented(t *testing.T) {
	in := correctedInputs("claude-opus-5", "claude-haiku-4-5", 200_000, 0, 0, false)
	in.CachePrefixKnown = false
	corrected := planner.Decide(in, correctedCfg(3))
	legacy := planner.Decide(in, legacyCfg(3))
	assert.InDelta(t, legacy.ExpectedSavingsUSD, corrected.ExpectedSavingsUSD, 1e-12,
		"k=1 must collapse the corrected rate back onto price*multiplier")
}

// A measured zero prefix is a real cold cache and must be priced as one.
// Without CachePrefixKnown the k=1 fallback swallows it, pricing the pin as
// fully cached and charging eviction for a prefix that does not exist.
func TestExplicitZeroPrefixIsNotTreatedAsFullyCached(t *testing.T) {
	measuredZero := correctedInputs("claude-opus-5", "claude-haiku-4-5", 200_000, 0, 0, false)
	measuredZero.CachePrefixKnown = true

	noEvidence := measuredZero
	noEvidence.CachePrefixKnown = false

	zero := planner.Decide(measuredZero, correctedCfg(3))
	absent := planner.Decide(noEvidence, correctedCfg(3))

	assert.Greater(t, zero.ExpectedSavingsUSD, absent.ExpectedSavingsUSD,
		"an uncached prompt is billed at full rate on both sides, so the 5x gap is undiscounted")
	assert.Zero(t, zero.EvictionCostUSD,
		"there is no live prefix to evict")
	assert.Greater(t, absent.EvictionCostUSD, 0.0,
		"the no-telemetry fallback still assumes a full prefix worth evicting")
}

// Eviction is (w-m) -- the write paid in place of the read -- not the (1-m)
// the legacy path charges.
func TestCorrectedEVChargesWritePremiumNotFullPrice(t *testing.T) {
	in := correctedInputs("claude-opus-5", "claude-haiku-4-5", 1_000_000, 1_000_000, 0, false)
	got := planner.Decide(in, correctedCfg(3)).EvictionCostUSD
	// haiku $1/Mtok, write 1.25x, read 0.10x, whole prompt cacheable.
	assert.InDelta(t, 1.0*(1.25-0.10), got, 1e-9)
	assert.False(t, math.IsNaN(got))
}

// Nothing live to destroy, so moving is free and both sides pay full rate.
func TestCorrectedEVColdPinChargesNoEviction(t *testing.T) {
	cold := planner.Decide(
		correctedInputs("claude-opus-5", "claude-haiku-4-5", 200_000, 180_000, 1200, true), correctedCfg(3))
	assert.Zero(t, cold.EvictionCostUSD)
	assert.Equal(t, planner.OutcomeSwitch, cold.Outcome)
}
