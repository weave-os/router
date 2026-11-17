package catalog

import (
	"weave-os/router/internal/providers"
	"weave-os/router/internal/usage"
)

type InferenceGeo = usage.InferenceGeo

const InferenceGeoUS = usage.InferenceGeoUS

type Speed = usage.Speed

const SpeedFast = usage.SpeedFast

type UsageModifiers struct {
	CacheCreation1h int
	InferenceGeo    InferenceGeo
}

const AnthropicCacheWrite1hMultiplier = 2.0

const USInferenceGeoMultiplier = 1.1

func (p Pricing) EffectiveCacheWrite1hMultiplier(upstreamProvider string) float64 {
	if providers.FamilyFor(upstreamProvider) == providers.FamilyAnthropic {
		return AnthropicCacheWrite1hMultiplier
	}
	return p.EffectiveCacheWriteMultiplier()
}

func (p Pricing) EffectiveUSInferenceGeoMultiplier() float64 {
	if p.USInferenceGeoMultiplier > 0 {
		return p.USInferenceGeoMultiplier
	}
	return 1
}

func (p Pricing) WithoutInferenceGeoPremium() Pricing {
	p.USInferenceGeoMultiplier = 0
	return p
}

func (p Pricing) inferenceGeoMultiplier(geo InferenceGeo) float64 {
	if geo == InferenceGeoUS {
		return p.EffectiveUSInferenceGeoMultiplier()
	}
	return 1
}
