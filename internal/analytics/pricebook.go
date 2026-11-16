package analytics

import "weave-os/router/internal/router/catalog"

type ModelPrice struct {
	ID            string          `json:"id"`
	Tier          string          `json:"tier"`
	ContextWindow int             `json:"context_window"`
	Providers     []ProviderPrice `json:"providers"`
}

type ProviderPrice struct {
	Provider                 string            `json:"provider"`
	InputUSDPer1M            float64           `json:"input_usd_per_1m"`
	OutputUSDPer1M           float64           `json:"output_usd_per_1m"`
	CacheWriteMultiplier     float64           `json:"cache_write_multiplier"`
	CacheWrite1hMultiplier   float64           `json:"cache_write_1h_multiplier"`
	CacheReadMultiplier      float64           `json:"cache_read_multiplier"`
	USInferenceGeoMultiplier float64           `json:"us_inference_geo_multiplier"`
	LongContext              *LongContextPrice `json:"long_context,omitempty"`
}

type LongContextPrice struct {
	ThresholdTokens        int     `json:"threshold_tokens"`
	InputUSDPer1M          float64 `json:"input_usd_per_1m"`
	OutputUSDPer1M         float64 `json:"output_usd_per_1m"`
	CacheWriteMultiplier   float64 `json:"cache_write_multiplier"`
	CacheWrite1hMultiplier float64 `json:"cache_write_1h_multiplier"`
	CacheReadMultiplier    float64 `json:"cache_read_multiplier"`
}

func PriceBook() []ModelPrice {
	out := make([]ModelPrice, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		bindings := make([]ProviderPrice, 0, len(m.Providers))
		for _, b := range m.Providers {
			price := ProviderPrice{
				Provider:                 b.Provider,
				InputUSDPer1M:            b.Price.InputUSDPer1M,
				OutputUSDPer1M:           b.Price.OutputUSDPer1M,
				CacheWriteMultiplier:     b.Price.EffectiveCacheWriteMultiplier(),
				CacheWrite1hMultiplier:   b.Price.EffectiveCacheWrite1hMultiplier(b.Provider),
				CacheReadMultiplier:      b.Price.EffectiveCacheReadMultiplier(),
				USInferenceGeoMultiplier: b.Price.EffectiveUSInferenceGeoMultiplier(),
			}
			if b.Price.LongContext != nil {
				long := b.Price.ForInputTokens(b.Price.LongContext.ThresholdTokens + 1)
				price.LongContext = &LongContextPrice{
					ThresholdTokens:        b.Price.LongContext.ThresholdTokens,
					InputUSDPer1M:          long.InputUSDPer1M,
					OutputUSDPer1M:         long.OutputUSDPer1M,
					CacheWriteMultiplier:   long.EffectiveCacheWriteMultiplier(),
					CacheWrite1hMultiplier: long.EffectiveCacheWrite1hMultiplier(b.Provider),
					CacheReadMultiplier:    long.EffectiveCacheReadMultiplier(),
				}
			}
			bindings = append(bindings, price)
		}
		out = append(out, ModelPrice{
			ID:            m.ID,
			Tier:          m.Tier.String(),
			ContextWindow: catalog.ContextWindowFor(m.ID),
			Providers:     bindings,
		})
	}
	return out
}
