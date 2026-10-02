package catalog

import "sort"

// ScopeCatalog is the `scope` query value of GET /v1/router/models that selects
// the full compile-time catalog instead of a serving strategy's roster.
const ScopeCatalog = "catalog"

// ModelListing is one wire row of GET /v1/router/models: the ID clients send,
// the primary provider the settings UI groups by, and whether the dashboard
// may offer a fast-mode toggle for the model.
type ModelListing struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	FastMode bool   `json:"fast_mode"`
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
		rows = append(rows, ModelListing{Model: m.ID, Provider: m.PrimaryProvider(), FastMode: SupportsFastMode(m.ID)})
	}
	SortListing(rows)
	return rows
}

// BindingListing reports every provider binding for internal control-plane
// eligibility checks. Public model lists retain their primary-provider shape.
func BindingListing() []ModelListing {
	rows := make([]ModelListing, 0, len(Models))
	for _, model := range Models {
		if model.ID == "" {
			continue
		}
		for _, binding := range model.Providers {
			rows = append(rows, ModelListing{
				Model: model.ID, Provider: binding.Provider, FastMode: SupportsFastMode(model.ID),
			})
		}
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
