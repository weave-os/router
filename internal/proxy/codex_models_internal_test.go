package proxy

import (
	"testing"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAddCodexRoutingModels_PreservesNativeCatalog(t *testing.T) {
	const upstream = `{"models":[{"slug":"gpt-6-sol","display_name":"GPT-6-Sol","visibility":"list","priority":2,"supported_reasoning_levels":[{"effort":"max"}],"context_window":272000},{"slug":"gpt-6-astra","display_name":"GPT-6-Astra","visibility":"list","priority":1}],"etag":"catalog-v2"}`
	merged, err := addCodexRoutingModels([]byte(upstream), false)
	require.NoError(t, err)
	assert.Equal(t, "catalog-v2", gjson.GetBytes(merged, "etag").String())
	assert.Equal(t, "gpt-6-astra", gjson.GetBytes(merged, "models.1.slug").String())
	assert.Equal(t, CodexAutomaticModel, gjson.GetBytes(merged, "models.2.slug").String())
	assert.Equal(t, "Weave Router", gjson.GetBytes(merged, "models.2.display_name").String())
	assert.Equal(t, "Automatically chooses the best model", gjson.GetBytes(merged, "models.2.description").String())
	assert.Equal(t, "list", gjson.GetBytes(merged, "models.2.visibility").String())
	assert.Equal(t, "max", gjson.GetBytes(merged, "models.2.supported_reasoning_levels.0.effort").String())
	assert.Equal(t, int64(272000), gjson.GetBytes(merged, "models.2.context_window").Int())
}

func TestAddCodexRoutingModels_UsesAvailableMetadataWithoutSol(t *testing.T) {
	merged, err := addCodexRoutingModels([]byte(`{"models":[{"slug":"gpt-6-astra","visibility":"list","context_window":272000}]}`), false)
	require.NoError(t, err)
	assert.Equal(t, CodexAutomaticModel, gjson.GetBytes(merged, "models.1.slug").String())
	assert.Equal(t, int64(272000), gjson.GetBytes(merged, "models.1.context_window").Int())
}

func TestAddCodexRoutingModels_RequiresAtLeastOneModel(t *testing.T) {
	_, err := addCodexRoutingModels([]byte(`{"models":[]}`), true)
	require.ErrorContains(t, err, "no model")
}

func TestAddCodexRoutingModels_AdvertisesInternalVariantsWithoutDuplicates(t *testing.T) {
	const upstream = `{"models":[{"slug":"gpt-6-sol","context_window":272000},{"slug":"weave-auto","display_name":"existing"}],"etag":"catalog-v2"}`
	merged, err := addCodexRoutingModels([]byte(upstream), true)
	require.NoError(t, err)
	require.Len(t, gjson.GetBytes(merged, "models").Array(), 5)
	require.Equal(t, "catalog-v2", gjson.GetBytes(merged, "etag").String())
	require.Equal(t, "existing", gjson.GetBytes(merged, "models.1.display_name").String())
	require.Equal(t, string(router.ModelHMM), gjson.GetBytes(merged, "models.2.slug").String())
	require.Equal(t, string(router.ModelQwen), gjson.GetBytes(merged, "models.3.slug").String())
	require.Equal(t, int64(272000), gjson.GetBytes(merged, "models.3.context_window").Int())
	require.Contains(t, gjson.GetBytes(merged, "models.3.description").String(), "Qwen classifier")
	require.Equal(t, string(router.ModelDecision), gjson.GetBytes(merged, "models.4.slug").String())
	repeated, err := addCodexRoutingModels(merged, true)
	require.NoError(t, err)
	require.JSONEq(t, string(merged), string(repeated))
}
