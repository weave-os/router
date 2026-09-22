package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAddCodexAutomaticModel_PreservesNativeCatalog(t *testing.T) {
	const upstream = `{"models":[{"slug":"gpt-6-sol","display_name":"GPT-6-Sol","visibility":"list","priority":2,"supported_reasoning_levels":[{"effort":"max"}],"context_window":272000},{"slug":"gpt-6-astra","display_name":"GPT-6-Astra","visibility":"list","priority":1}],"etag":"catalog-v2"}`
	merged, err := addCodexAutomaticModel([]byte(upstream))
	require.NoError(t, err)
	assert.Equal(t, "catalog-v2", gjson.GetBytes(merged, "etag").String())
	assert.Equal(t, "gpt-6-astra", gjson.GetBytes(merged, "models.1.slug").String())
	assert.Equal(t, CodexAutomaticModel, gjson.GetBytes(merged, "models.2.slug").String())
	assert.Equal(t, "list", gjson.GetBytes(merged, "models.2.visibility").String())
	assert.Equal(t, "max", gjson.GetBytes(merged, "models.2.supported_reasoning_levels.0.effort").String())
	assert.Equal(t, int64(272000), gjson.GetBytes(merged, "models.2.context_window").Int())
}

func TestAddCodexAutomaticModel_UsesAvailableMetadataWithoutSol(t *testing.T) {
	merged, err := addCodexAutomaticModel([]byte(`{"models":[{"slug":"gpt-6-astra","visibility":"list","context_window":272000}]}`))
	require.NoError(t, err)
	assert.Equal(t, CodexAutomaticModel, gjson.GetBytes(merged, "models.1.slug").String())
	assert.Equal(t, int64(272000), gjson.GetBytes(merged, "models.1.context_window").Int())
}

func TestAddCodexAutomaticModel_RequiresAtLeastOneModel(t *testing.T) {
	_, err := addCodexAutomaticModel([]byte(`{"models":[]}`))
	require.ErrorContains(t, err, "no model")
}
