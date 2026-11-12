package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/catalog"
)

func TestCatalogRosterSource_ListsEveryCatalogModel(t *testing.T) {
	entries, err := catalogRosterSource{}.HMMDeployedModels(context.Background())
	require.NoError(t, err)

	listing := catalog.Listing()
	require.NotEmpty(t, listing)
	require.Len(t, entries, len(listing))
	for i, row := range listing {
		assert.Equal(t, row.Model, entries[i].Model)
		assert.Equal(t, row.Provider, entries[i].Provider)
	}
}
