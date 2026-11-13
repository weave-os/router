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
