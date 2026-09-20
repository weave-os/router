package catalog

import (
	"math"
	"sync"

	"weave-os/router/internal/providers"
)

// EffectiveInputCost returns the true USD input cost after applying cache
// pricing. Fresh tokens at base rate; cache-creation at the binding's
// effective write multiplier; cache-read at the binding's effective read
// multiplier. upstreamProvider distinguishes
// Anthropic (input_tokens is fresh-only) from OpenAI / Gemini
// (prompt_tokens includes cached tokens — must subtract).
//
// Single source of truth for the proxy's OTel emitter, telemetry write
// path, and the billing debit hook.
func EffectiveInputCost(inputTokens, cacheCreation, cacheRead int, p Pricing, upstreamProvider string) float64 {
	p = p.ForInputTokens(inputTokens)
	fresh := inputTokens
	if upstreamProvider != providers.ProviderAnthropic {
		fresh = inputTokens - cacheCreation - cacheRead
	}
	if fresh < 0 {
		fresh = 0
	}
	return (float64(fresh) +
		float64(cacheCreation)*p.EffectiveCacheWriteMultiplier() +
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

// upperBoundOutputTokens caps the completion half of a worst-case turn.
// A turn's input is bounded by the model's context window, but its output is
// only bounded by the client's max_tokens, which is not known before dispatch,
// so the bound is the highest output cap any deployed model accepts (Grok and
// Muse clamp at 131072; the gpt-5.x family at 128000).
const upperBoundOutputTokens = 131_072

// TurnUpperBoundUsdMicros is the most a single turn can retail for across the
// catalog: the priciest model's whole context window billed as fresh input,
// plus a full completion.
//
// Allowance reservations need a bound that holds before the model is chosen
// and before any token is counted. Using an expected cost instead would let
// concurrent turns each pass a check only the first of them can afford, which
// is the overrun the reservation exists to close; the bound is returned to the
// windows at finalization, so over-reserving costs headroom only while the
// turn is in flight.
var TurnUpperBoundUsdMicros = sync.OnceValue(func() int64 {
	var worst float64
	for _, m := range Models {
		price, ok := PrimaryPriceFor(m.ID)
		if !ok {
			continue
		}
		inputTokens := m.ContextWindow
		if inputTokens <= 0 {
			inputTokens = DefaultContextWindow
		}
		cost := EffectiveInputCost(inputTokens, 0, 0, price, providers.ProviderAnthropic) +
			EffectiveOutputCost(inputTokens, upperBoundOutputTokens, price)
		if cost > worst {
			worst = cost
		}
	}
	return USDToMicros(worst)
})

// SignedUSDToMicros is USDToMicros without the negative clamp; planner EV terms are signed.
// NaN/Inf still collapse to 0 so non-finite values cannot persist as BIGINT garbage.
func SignedUSDToMicros(f float64) int64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int64(math.Round(f * 1_000_000))
}
