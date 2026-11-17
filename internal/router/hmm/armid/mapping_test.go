package armid

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/catalog"
)

func TestGPT61SolRosterIdentity(t *testing.T) {
	model, ok := catalog.ByID("gpt-6.1-sol")
	require.True(t, ok)
	assert.Equal(t, "openai/gpt-6.1-sol", ForModel(model))
	assert.Equal(t, "gpt-6.1-sol", CatalogIDForRoster("openai/gpt-6.1-sol"))
	assert.Empty(t, ValidateRosterIDs([]string{"openai/gpt-6.1-sol"}))
}

func TestGPT62RosterIdentity(t *testing.T) {
	for _, id := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		model, ok := catalog.ByID(id)
		require.True(t, ok, id)
		assert.Equal(t, "openai/"+id, ForModel(model))
		assert.Equal(t, id, CatalogIDForRoster("openai/"+id))
		assert.Empty(t, ValidateRosterIDs([]string{"openai/" + id}))
	}
}
