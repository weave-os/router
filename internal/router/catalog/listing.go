package catalog

import "sort"

const ScopeCatalog = "catalog"

type ModelListing struct {
	Model              string  `json:"model"`
	Provider           string  `json:"provider"`
	FastMode           bool    `json:"fast_mode"`
	InputUSDPer1M      float64 `json:"input_usd_per_1m,omitempty"`
	OutputUSDPer1M     float64 `json:"output_usd_per_1m,omitempty"`
	FastInputUSDPer1M  float64 `json:"fast_input_usd_per_1m,omitempty"`
	FastOutputUSDPer1M float64 `json:"fast_output_usd_per_1m,omitempty"`
}

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

type ModelListingResponse struct {
	Models []ModelListing `json:"models"`
}

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

func SortListing(rows []ModelListing) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Provider != rows[j].Provider {
			return rows[i].Provider < rows[j].Provider
		}
		return rows[i].Model < rows[j].Model
	})
}
