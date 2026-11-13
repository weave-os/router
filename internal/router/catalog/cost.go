package catalog

import (
	"math"

	"weave-os/router/internal/providers"
)

// EffectiveInputCost returns the true USD input cost after applying cache
// pricing. Fresh tokens at base rate; 5-minute cache-creation at the binding's
// effective write multiplier; 1-hour cache-creation (usageModifiers.CacheCreation1h, a
// subset of cacheCreation) at the 1-hour write multiplier; cache-read at the
// binding's effective read multiplier; the whole sum scaled by the US
// inference-geography multiplier when it applies. upstreamProvider's wire
// family distinguishes Anthropic-spec upstreams (input_tokens is fresh-only)
// from OpenAI / Gemini (prompt_tokens includes cached tokens — must subtract).
//
// Single source of truth for the proxy's OTel emitter, telemetry write
// path, and the billing debit hook.
func EffectiveInputCost(inputTokens, cacheCreation, cacheRead int, p Pricing, upstreamProvider string, usageModifiers UsageModifiers) float64 {
	p = p.ForInputTokens(PromptTokens(inputTokens, cacheCreation, cacheRead, upstreamProvider))
	fresh := inputTokens
	if providers.FamilyFor(upstreamProvider) != providers.FamilyAnthropic {
		fresh = inputTokens - cacheCreation - cacheRead
	}
	if fresh < 0 {
		fresh = 0
	}
	oneHour := min(max(usageModifiers.CacheCreation1h, 0), max(cacheCreation, 0))
	fiveMinute := cacheCreation - oneHour
	return (float64(fresh) +
		float64(fiveMinute)*p.EffectiveCacheWriteMultiplier() +
		float64(oneHour)*p.EffectiveCacheWrite1hMultiplier(upstreamProvider) +
		float64(cacheRead)*p.EffectiveCacheReadMultiplier()) / 1_000_000 * p.InputUSDPer1M * p.inferenceGeoMultiplier(usageModifiers.InferenceGeo)
}

// CounterfactualInputCost is EffectiveInputCost for the savings baseline, with
// warmPrefill of the cache-creation tokens priced as cache reads: a baseline
// that never switched models would have read that prefix from a warm cache.
// The remaining writes keep the observed 5-minute/1-hour proportion.
func CounterfactualInputCost(inputTokens, cacheCreation, cacheRead, warmPrefill int, p Pricing, upstreamProvider string, usageModifiers UsageModifiers) float64 {
	remaining := cacheCreation - warmPrefill
	if cacheCreation > 0 && usageModifiers.CacheCreation1h > 0 {
		usageModifiers.CacheCreation1h = int(math.Round(float64(usageModifiers.CacheCreation1h) * float64(remaining) / float64(cacheCreation)))
	}
	return EffectiveInputCost(inputTokens, remaining, cacheRead+warmPrefill, p, upstreamProvider, usageModifiers)
}

// PromptTokens returns the full prompt length that selects a LongContext
// tier. Anthropic-family input_tokens counts only uncached tokens, so the
// cache writes and reads are added back; other families already include them.
func PromptTokens(inputTokens, cacheCreation, cacheRead int, upstreamProvider string) int {
	if providers.FamilyFor(upstreamProvider) != providers.FamilyAnthropic {
		return inputTokens
	}
	return inputTokens + max(cacheCreation, 0) + max(cacheRead, 0)
}

// EffectiveOutputCost returns USD output cost for a call. Output tokens
// have no caching multipliers — tokens × per-1M price, scaled by the US
// inference-geography multiplier when it applies. promptTokens is the full
// prompt length (see PromptTokens) and only selects the LongContext tier.
func EffectiveOutputCost(promptTokens, outputTokens int, p Pricing, usageModifiers UsageModifiers) float64 {
	p = p.ForInputTokens(promptTokens)
	return float64(outputTokens) / 1_000_000 * p.OutputUSDPer1M * p.inferenceGeoMultiplier(usageModifiers.InferenceGeo)
}

// USDToMicros rounds a float64 USD value to BIGINT micros (USD x 1e6) for
// persistence/debit math. NaN, Inf, and negative values collapse to 0 — we
// never want to write nonsense or debit/charge a negative amount.
//
// Single source of truth for the billing debit hook's notional-cost math
// and the telemetry write path's stored cost columns; both used to
// hand-roll this rounding independently.
func USDToMicros(f float64) int64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	return int64(math.Round(f * 1_000_000))
}

// SignedUSDToMicros is USDToMicros without the negative clamp; planner EV terms are signed.
// NaN/Inf still collapse to 0 so non-finite values cannot persist as BIGINT garbage.
func SignedUSDToMicros(f float64) int64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int64(math.Round(f * 1_000_000))
}
