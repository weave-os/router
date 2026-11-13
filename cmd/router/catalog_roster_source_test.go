package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"
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

func TestDeployedModelsSource_ManagedAdmissionListsCatalogUnauthed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	server.RegisterWithFeatures(engine, nil, nil, deployedModelsSource(nil), server.DeploymentModeManaged, nil, nil, nil, nil, server.Features{ServingAdmission: &middleware.ServingAdmissionConfig{}})

	for _, path := range []string{"/v1/router/models", "/v1/router/models?strategy=hmm", "/v1/router/models?strategy=hmm_embedding"} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, response.Code, path)
		var body catalog.ModelListingResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body), path)
		assert.Equal(t, catalog.Listing(), body.Models, path)
	}
}

func TestDeployedModelsSource_KeepsConfiguredRoster(t *testing.T) {
	configured := newHMMRosterSource(&stubRosterFetcher{}, 0)
	assert.Same(t, configured, deployedModelsSource(configured))
}
