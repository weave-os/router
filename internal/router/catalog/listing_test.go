package catalog

import (
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListingPricesEveryRowFromItsPrimaryBinding(t *testing.T) {
	t.Parallel()
	rows := Listing()
	require.NotEmpty(t, rows)
	for _, row := range rows {
		m, ok := ByID(row.Model)
		require.True(t, ok, row.Model)
		primary := m.Providers[0]
		assert.Positive(t, row.InputUSDPer1M, row.Model)
		assert.Positive(t, row.OutputUSDPer1M, row.Model)
		assert.Equal(t, primary.Price.InputUSDPer1M, row.InputUSDPer1M, row.Model)
		assert.Equal(t, primary.Price.OutputUSDPer1M, row.OutputUSDPer1M, row.Model)
		fastPrice, hasFastPrice := primary.FastPricing()
		if hasFastPrice {
			assert.Equal(t, fastPrice.InputUSDPer1M, row.FastInputUSDPer1M, row.Model)
			assert.Equal(t, fastPrice.OutputUSDPer1M, row.FastOutputUSDPer1M, row.Model)
		} else {
			assert.Zero(t, row.FastInputUSDPer1M, row.Model)
			assert.Zero(t, row.FastOutputUSDPer1M, row.Model)
		}
	}
}

func TestNewModelListingPricesTheListedProvidersBinding(t *testing.T) {
	t.Parallel()
	for _, m := range Models {
		for _, binding := range m.Providers {
			row := NewModelListing(m.ID, binding.Provider)
			assert.Equal(t, binding.Price.InputUSDPer1M, row.InputUSDPer1M, "%s via %s", m.ID, binding.Provider)
			assert.Equal(t, binding.Price.OutputUSDPer1M, row.OutputUSDPer1M, "%s via %s", m.ID, binding.Provider)
			fastPrice, hasFastPrice := binding.FastPricing()
			if hasFastPrice {
				assert.Equal(t, fastPrice.InputUSDPer1M, row.FastInputUSDPer1M, "%s via %s", m.ID, binding.Provider)
				assert.Equal(t, fastPrice.OutputUSDPer1M, row.FastOutputUSDPer1M, "%s via %s", m.ID, binding.Provider)
			} else {
				assert.Zero(t, row.FastInputUSDPer1M, "%s via %s", m.ID, binding.Provider)
				assert.Zero(t, row.FastOutputUSDPer1M, "%s via %s", m.ID, binding.Provider)
			}
		}
	}
}

func TestNewModelListingLeavesUnpricedModelsWithoutPrices(t *testing.T) {
	t.Parallel()
	row := NewModelListing("not-a-catalog-model", providers.ProviderAnthropic)
	assert.Equal(t, "not-a-catalog-model", row.Model)
	assert.Zero(t, row.InputUSDPer1M)
	assert.Zero(t, row.OutputUSDPer1M)
	assert.Zero(t, row.FastInputUSDPer1M)
	assert.Zero(t, row.FastOutputUSDPer1M)
}
