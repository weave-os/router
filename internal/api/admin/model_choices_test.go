package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCatalogModelsHandler_SelectionDescribesModelIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hmm := &fakeHMMRoster{}
	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(fakeDeployedModels{}, hmm))
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/router/models?scope=selection", nil))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Zero(t, hmm.calls, "descriptions must remain available when policy inference is unavailable")
	var choices catalog.ModelChoicesResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &choices))
	seen := make(map[string]catalog.ModelChoice)
	for _, choice := range choices.Models {
		require.NotEmpty(t, choice.Description)
		seen[choice.Model] = choice
	}
	require.Equal(t, catalog.ModelChoiceRouter, seen[string(router.ModelHMM)].Kind)
	require.Contains(t, seen[string(router.ModelQwen)].Description, "Qwen classifier")
	require.Equal(t, catalog.ModelChoiceDirect, seen["gpt-6.1-sol"].Kind)
}
