package catalog

import (
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListingPricesEveryRowFromItsPrimaryBinding(t *testing.T) {
	rows := Listing()
	require.NotEmpty(t, rows)
	for _, row := range rows {
		m, ok := ByID(row.Model)
		require.True(t, ok, row.Model)
		primary := m.Providers[0].Price
		assert.Positive(t, row.InputUSDPer1M, row.Model)
		assert.Positive(t, row.OutputUSDPer1M, row.Model)
		assert.Equal(t, primary.InputUSDPer1M, row.InputUSDPer1M, row.Model)
		assert.Equal(t, primary.OutputUSDPer1M, row.OutputUSDPer1M, row.Model)
	}
}

func TestNewModelListingPricesTheListedProvidersBinding(t *testing.T) {
	for _, m := range Models {
		for _, binding := range m.Providers {
			row := NewModelListing(m.ID, binding.Provider)
			assert.Equal(t, binding.Price.InputUSDPer1M, row.InputUSDPer1M, "%s via %s", m.ID, binding.Provider)
			assert.Equal(t, binding.Price.OutputUSDPer1M, row.OutputUSDPer1M, "%s via %s", m.ID, binding.Provider)
		}
	}
}

func TestNewModelListingLeavesUnpricedModelsWithoutPrices(t *testing.T) {
	row := NewModelListing("not-a-catalog-model", providers.ProviderAnthropic)
	assert.Equal(t, "not-a-catalog-model", row.Model)
	assert.Zero(t, row.InputUSDPer1M)
	assert.Zero(t, row.OutputUSDPer1M)
}
