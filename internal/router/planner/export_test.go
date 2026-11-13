package planner

import "weave-os/router/internal/router/catalog"

// CorrectedTermsForTest exposes corrected per-turn gain and switch cost for
// cross-validation against router-internal/eval/cache_eviction/policy.py.
// Test-only.
func CorrectedTermsForTest(
	pin, fresh catalog.Pricing, tokens, k, priorOutput float64, cold bool,
) (gain, switchCost float64) {
	in := Inputs{
		EstimatedInputTokens:  int(tokens),
		CacheablePrefixTokens: int(tokens * k),
		CachePrefixKnown:      true,
		PriorOutputTokens:     int(priorOutput),
		PinCacheCold:          cold,
	}
	return correctedEV(pin, fresh, in, EVConfig{ExpectedRemainingTurns: 1})
}
