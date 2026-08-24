package planner

import "workweave/router/internal/router/catalog"

// CorrectedTermsForTest exposes the corrected per-turn gain and switch cost so
// the external test package can cross-validate them against the Python
// reference (router-internal/eval/cache_eviction/policy.py) that the replay
// results were produced with. Test-only.
func CorrectedTermsForTest(
	pin, fresh catalog.Pricing, tokens, k, priorOutput float64, cold bool,
) (gain, switchCost float64) {
	in := Inputs{
		EstimatedInputTokens:  int(tokens),
		CacheablePrefixTokens: int(tokens * k),
		PriorOutputTokens:     int(priorOutput),
		PinCacheCold:          cold,
	}
	return correctedEV(pin, fresh, in, EVConfig{ExpectedRemainingTurns: 1})
}
