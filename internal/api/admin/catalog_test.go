package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogModelsHandler_SortsByProviderThenModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	src := &fakeHMMRoster{entries: []router.DeployedEntry{
		{Model: "gpt-5.5", Provider: providers.ProviderOpenAI},
		{Model: "claude-opus-4-7", Provider: providers.ProviderAnthropic},
		{Model: "claude-haiku-4-5", Provider: providers.ProviderAnthropic},
		{Model: "gpt-5.4-mini", Provider: providers.ProviderOpenAI},
	}}

	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(src))

	req := httptest.NewRequest(http.MethodGet, "/v1/router/models", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var got admin.CatalogModelsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	require.Len(t, got.Models, 4)
	assert.Equal(t, providers.ProviderAnthropic, got.Models[0].Provider)
	assert.Equal(t, "claude-haiku-4-5", got.Models[0].Model)
	assert.Equal(t, providers.ProviderAnthropic, got.Models[1].Provider)
	assert.Equal(t, "claude-opus-4-7", got.Models[1].Model)
	assert.Equal(t, providers.ProviderOpenAI, got.Models[2].Provider)
	assert.Equal(t, "gpt-5.4-mini", got.Models[2].Model)
	assert.Equal(t, providers.ProviderOpenAI, got.Models[3].Provider)
	assert.Equal(t, "gpt-5.5", got.Models[3].Model)
}

func TestCatalogModelsHandler_EmptyListReturnsEmptyArray(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(&fakeHMMRoster{}))

	req := httptest.NewRequest(http.MethodGet, "/v1/router/models", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	// Empty slice must round-trip as [], not null — the Weave control plane
	// distinguishes "no models" from "missing field".
	assert.JSONEq(t, `{"models":[]}`, rec.Body.String())
}

type fakeHMMRoster struct {
	entries []router.DeployedEntry
	err     error
	calls   int
}

func (f *fakeHMMRoster) HMMDeployedModels(context.Context) ([]router.DeployedEntry, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

func TestCatalogModelsHandler_ServesHMMRosterForEveryStrategy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hmmSrc := &fakeHMMRoster{entries: []router.DeployedEntry{
		{Model: "gpt-5.6-sol", Provider: providers.ProviderOpenAI},
		{Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic},
	}}

	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(hmmSrc))

	for _, query := range []string{"", "?strategy=hmm", "?strategy=hmm_embedding", "?strategy=cluster"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/router/models"+query, nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, query)
		var got admin.CatalogModelsResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Len(t, got.Models, 2, query)
		assert.Equal(t, "claude-opus-4-8", got.Models[0].Model, query)
		assert.Equal(t, "gpt-5.6-sol", got.Models[1].Model, query)
	}
	assert.Equal(t, 4, hmmSrc.calls, "the default universe must come from the HMM roster, not a legacy scorer")
}

// The weave control plane decodes this body into {model, provider, fast_mode}
// rows to validate exclusions; renaming or adding a field breaks that client.
func TestCatalogModelsHandler_WireShapeContract(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hmmSrc := &fakeHMMRoster{entries: []router.DeployedEntry{
		{Model: "gpt-5.4-mini", Provider: providers.ProviderOpenAI},
		{Model: "claude-opus-4-6", Provider: providers.ProviderAnthropic},
	}}
	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(hmmSrc))

	req := httptest.NewRequest(http.MethodGet, "/v1/router/models", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string][]map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body, 1, "top level carries only models")
	rows := body["models"]
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.Len(t, row, 3, "each row is exactly {model, provider, fast_mode}: %v", row)
		assert.IsType(t, "", row["model"])
		assert.IsType(t, "", row["provider"])
		assert.IsType(t, false, row["fast_mode"])
	}
	assert.Equal(t, "claude-opus-4-6", rows[0]["model"])
	assert.Equal(t, "anthropic", rows[0]["provider"])
	assert.Equal(t, "gpt-5.4-mini", rows[1]["model"])
	assert.Equal(t, "openai", rows[1]["provider"])
}

func TestCatalogModelsHandler_HMMRosterErrorReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hmmSrc := &fakeHMMRoster{err: errors.New("sidecar unavailable")}

	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(hmmSrc))

	req := httptest.NewRequest(http.MethodGet, "/v1/router/models?strategy=hmm_embedding", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestCatalogModelsHandler_ScopeCatalogReturnsFullCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hmmSrc := &fakeHMMRoster{entries: []router.DeployedEntry{
		{Model: "gpt-5.6-sol", Provider: providers.ProviderOpenAI},
	}}

	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(hmmSrc))

	req := httptest.NewRequest(http.MethodGet, "/v1/router/models?scope=catalog&strategy=hmm", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, hmmSrc.calls, "scope=catalog must not consult the HMM roster")

	var got admin.CatalogModelsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Greater(t, len(got.Models), 1, "full catalog is larger than the 1-entry roster fixture")

	seen := map[string]struct{}{}
	for _, m := range got.Models {
		require.NotEmpty(t, m.Model)
		require.NotEmpty(t, m.Provider)
		_, dup := seen[m.Model]
		require.False(t, dup, "duplicate catalog id %s", m.Model)
		seen[m.Model] = struct{}{}
	}
	_, hasOpus := seen["claude-opus-5"]
	require.True(t, hasOpus, "catalog must include a known Models row")
}
