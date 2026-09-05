package analytics

import "weave-os/router/internal/router/catalog"

// ModelPrice is one model's export-facing price entry. Published so a consumer
// can recompute cost columns independently.
type ModelPrice struct {
	ID            string          `json:"id"`
	Tier          string          `json:"tier"`
	ContextWindow int             `json:"context_window"`
	Providers     []ProviderPrice `json:"providers"`
}

// ProviderPrice is one (provider, model) price binding. The router prefers
// providers in this order; the first enabled one priced the turn.
type ProviderPrice struct {
	Provider             string            `json:"provider"`
	InputUSDPer1M        float64           `json:"input_usd_per_1m"`
	OutputUSDPer1M       float64           `json:"output_usd_per_1m"`
	CacheWriteMultiplier float64           `json:"cache_write_multiplier"`
	CacheReadMultiplier  float64           `json:"cache_read_multiplier"`
	LongContext          *LongContextPrice `json:"long_context,omitempty"`
}

// LongContextPrice is the alternate rate tier above ThresholdTokens.
type LongContextPrice struct {
	ThresholdTokens      int     `json:"threshold_tokens"`
	InputUSDPer1M        float64 `json:"input_usd_per_1m"`
	OutputUSDPer1M       float64 `json:"output_usd_per_1m"`
	CacheWriteMultiplier float64 `json:"cache_write_multiplier"`
	CacheReadMultiplier  float64 `json:"cache_read_multiplier"`
}

// PriceBook returns current prices for every model the router knows. These are
// today's prices, not those in force when a given row was recorded; per-row
// cost columns remain the authoritative record.
func PriceBook() []ModelPrice {
	out := make([]ModelPrice, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		bindings := make([]ProviderPrice, 0, len(m.Providers))
		for _, b := range m.Providers {
			price := ProviderPrice{
				Provider:             b.Provider,
				InputUSDPer1M:        b.Price.InputUSDPer1M,
				OutputUSDPer1M:       b.Price.OutputUSDPer1M,
				CacheWriteMultiplier: b.Price.EffectiveCacheWriteMultiplier(),
				CacheReadMultiplier:  b.Price.EffectiveCacheReadMultiplier(),
			}
			if b.Price.LongContext != nil {
				long := b.Price.ForInputTokens(b.Price.LongContext.ThresholdTokens + 1)
				price.LongContext = &LongContextPrice{
					ThresholdTokens:      b.Price.LongContext.ThresholdTokens,
					InputUSDPer1M:        long.InputUSDPer1M,
					OutputUSDPer1M:       long.OutputUSDPer1M,
					CacheWriteMultiplier: long.EffectiveCacheWriteMultiplier(),
					CacheReadMultiplier:  long.EffectiveCacheReadMultiplier(),
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
