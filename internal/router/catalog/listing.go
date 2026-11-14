package catalog

import "sort"

// ScopeCatalog is the `scope` query value of GET /v1/router/models that selects
// the full compile-time catalog instead of a serving strategy's roster.
const ScopeCatalog = "catalog"

// ModelListing is one wire row of GET /v1/router/models: the ID clients send,
// the primary provider the settings UI groups by, whether the dashboard may
// offer a fast-mode toggle for the model, and the listed provider's base and
// fast per-1M-token prices so model pickers never keep their own price table.
// Prices are omitted for a roster model the catalog cannot price.
type ModelListing struct {
	Model              string  `json:"model"`
	Provider           string  `json:"provider"`
	FastMode           bool    `json:"fast_mode"`
	InputUSDPer1M      float64 `json:"input_usd_per_1m,omitempty"`
	OutputUSDPer1M     float64 `json:"output_usd_per_1m,omitempty"`
	FastInputUSDPer1M  float64 `json:"fast_input_usd_per_1m,omitempty"`
	FastOutputUSDPer1M float64 `json:"fast_output_usd_per_1m,omitempty"`
}

// NewModelListing builds the wire row for a model served by provider, priced
// from that provider's binding.
func NewModelListing(model, provider string) ModelListing {
	price, _ := PriceFor(provider, model)
	fastPrice, _ := FastPriceFor(provider, model)
	return ModelListing{
		Model:              model,
		Provider:           provider,
		FastMode:           SupportsFastMode(model),
		InputUSDPer1M:      price.InputUSDPer1M,
		OutputUSDPer1M:     price.OutputUSDPer1M,
		FastInputUSDPer1M:  fastPrice.InputUSDPer1M,
		FastOutputUSDPer1M: fastPrice.OutputUSDPer1M,
	}
}

// ModelListingResponse is the body of GET /v1/router/models. Kept stable so
// the Weave control plane can rely on the wire format without re-checking the
// artifact JSON shape on every router gitlink bump.
type ModelListingResponse struct {
	Models []ModelListing `json:"models"`
}

// Listing projects every catalog model to its wire row, sorted by provider
// then model. It is strategy- and lane-independent, so the gateway and every
// worker answer ?scope=catalog identically from the same compiled table.
func Listing() []ModelListing {
	rows := make([]ModelListing, 0, len(Models))
	for _, m := range Models {
		if m.ID == "" {
			continue
		}
		rows = append(rows, NewModelListing(m.ID, m.PrimaryProvider()))
	}
	SortListing(rows)
	return rows
}

// SortListing orders rows by provider then model, the order every models
// response uses so grouping in the settings UI stays consistent.
func SortListing(rows []ModelListing) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Provider != rows[j].Provider {
			return rows[i].Provider < rows[j].Provider
		}
		return rows[i].Model < rows[j].Model
	})
}
