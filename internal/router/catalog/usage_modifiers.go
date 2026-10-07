package catalog

import "weave-os/router/internal/providers"

// InferenceGeo is the inference geography a provider reports it served a call
// from (Anthropic usage.inference_geo).
type InferenceGeo string

// InferenceGeoUS is Anthropic's US-only inference geography.
const InferenceGeoUS InferenceGeo = "us"

// Speed is the serving speed a provider reports it billed a call at
// (Anthropic usage.speed).
type Speed string

// SpeedFast is Anthropic's fast-mode serving speed.
const SpeedFast Speed = "fast"

// UsageModifiers are provider-reported attributes of a call that change its
// per-token rates without changing its token counts. The zero value prices
// every cache write at the 5-minute rate with global inference.
type UsageModifiers struct {
	// CacheCreation1h is the share of the call's cache-creation tokens
	// written with the 1-hour TTL.
	CacheCreation1h int
	InferenceGeo    InferenceGeo
}

// AnthropicCacheWrite1hMultiplier is Anthropic's 1-hour cache-write price
// relative to base input price.
const AnthropicCacheWrite1hMultiplier = 2.0

// USInferenceGeoMultiplier is Anthropic's premium on every token category for
// US-only inference on the models that support it.
const USInferenceGeoMultiplier = 1.1

// EffectiveCacheWrite1hMultiplier returns the 1-hour cache-write multiplier:
// Anthropic's published rate on Anthropic-spec upstreams, else the 5-minute
// rate since no other family distinguishes write TTLs.
func (p Pricing) EffectiveCacheWrite1hMultiplier(upstreamProvider string) float64 {
	if providers.FamilyFor(upstreamProvider) == providers.FamilyAnthropic {
		return AnthropicCacheWrite1hMultiplier
	}
	return p.EffectiveCacheWriteMultiplier()
}

// EffectiveUSInferenceGeoMultiplier returns the binding's US-inference
// multiplier, or 1 when it charges no geography premium.
func (p Pricing) EffectiveUSInferenceGeoMultiplier() float64 {
	if p.USInferenceGeoMultiplier > 0 {
		return p.USInferenceGeoMultiplier
	}
	return 1
}

// WithoutInferenceGeoPremium returns p for a binding that borrows a
// first-party list price (a custom endpoint, or an unbound provider): only
// first-party Anthropic charges the US-inference premium.
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
