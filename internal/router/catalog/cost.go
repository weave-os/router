package catalog

import (
	"math"

	"weave-os/router/internal/providers"
)

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

func CounterfactualInputCost(inputTokens, cacheCreation, cacheRead, warmPrefill int, p Pricing, upstreamProvider string, usageModifiers UsageModifiers) float64 {
	remaining := cacheCreation - warmPrefill
	if cacheCreation > 0 && usageModifiers.CacheCreation1h > 0 {
		usageModifiers.CacheCreation1h = int(math.Round(float64(usageModifiers.CacheCreation1h) * float64(remaining) / float64(cacheCreation)))
	}
	return EffectiveInputCost(inputTokens, remaining, cacheRead+warmPrefill, p, upstreamProvider, usageModifiers)
}

func PromptTokens(inputTokens, cacheCreation, cacheRead int, upstreamProvider string) int {
	if providers.FamilyFor(upstreamProvider) != providers.FamilyAnthropic {
		return inputTokens
	}
	return inputTokens + max(cacheCreation, 0) + max(cacheRead, 0)
}

func EffectiveOutputCost(promptTokens, outputTokens int, p Pricing, usageModifiers UsageModifiers) float64 {
	p = p.ForInputTokens(promptTokens)
	return float64(outputTokens) / 1_000_000 * p.OutputUSDPer1M * p.inferenceGeoMultiplier(usageModifiers.InferenceGeo)
}

func USDToMicros(f float64) int64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	return int64(math.Round(f * 1_000_000))
}

func SignedUSDToMicros(f float64) int64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int64(math.Round(f * 1_000_000))
}
