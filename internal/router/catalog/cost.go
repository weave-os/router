package catalog

import (
	"math"

	"weave-os/router/internal/providers"
)

// EffectiveInputCost returns the true USD input cost after applying cache
// pricing. Fresh tokens at base rate; 5-minute cache-creation at the binding's
// effective write multiplier; 1-hour cache-creation (Anthropic's ttl "1h"
// tier) at the binding's effective 1-hour write multiplier; cache-read at the
// binding's effective read multiplier. cacheCreation1h is the portion of
// cacheCreation written on the 1-hour tier; 0 prices every write at the
// 5-minute rate, preserving aggregate-only payloads (Bedrock/Vertex).
// upstreamProvider distinguishes Anthropic (input_tokens is fresh-only) from
// OpenAI / Gemini (prompt_tokens includes cached tokens — must subtract).
//
// Single source of truth for the proxy's OTel emitter, telemetry write
// path, and the billing debit hook.
func EffectiveInputCost(inputTokens, cacheCreation, cacheCreation1h, cacheRead int, p Pricing, upstreamProvider string) float64 {
	p = p.ForInputTokens(inputTokens)
	fresh := inputTokens
	if upstreamProvider != providers.ProviderAnthropic {
		fresh = inputTokens - cacheCreation - cacheRead
	}
	if fresh < 0 {
		fresh = 0
	}
	if cacheCreation1h > cacheCreation {
		// Inconsistent payload (1h breakdown above the aggregate) — never let
		// the 5-minute remainder go negative and under-price the turn.
		cacheCreation1h = cacheCreation
	}
	cacheCreation5m := cacheCreation - cacheCreation1h
	return (float64(fresh) +
		float64(cacheCreation5m)*p.EffectiveCacheWriteMultiplier() +
		float64(cacheCreation1h)*p.EffectiveCacheWriteMultiplier1h() +
		float64(cacheRead)*p.EffectiveCacheReadMultiplier()) / 1_000_000 * p.InputUSDPer1M
}

// EffectiveOutputCost returns USD output cost for a call. Output tokens
// have no caching multipliers — straight tokens × per-1M price.
func EffectiveOutputCost(inputTokens, outputTokens int, p Pricing) float64 {
	p = p.ForInputTokens(inputTokens)
	return float64(outputTokens) / 1_000_000 * p.OutputUSDPer1M
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
