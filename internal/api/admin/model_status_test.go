package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"workweave/router/internal/api/admin"
	"workweave/router/internal/providers"
	"workweave/router/internal/router/modelstatus"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modelStatusFixture() *modelstatus.Store {
	store := modelstatus.New(func() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) }, time.Minute, 5*time.Minute, nil)
	store.Initialize(context.Background(), modelstatus.Key{ModelID: "claude-sonnet-4-5", Provider: providers.ProviderAnthropic}, true)
	return store
}

func TestGetModelStatusHandlerFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/model-status", admin.GetModelStatusHandler(modelStatusFixture()))
	request := httptest.NewRequest(http.MethodGet, "/model-status?provider=anthropic&status=online", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var body struct {
		Total   int `json:"total"`
		Entries []struct {
			ModelID  string `json:"model_id"`
			Provider string `json:"provider"`
			Status   string `json:"status"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body.Entries, 1)
	assert.Equal(t, 1, body.Total)
	assert.Equal(t, "claude-sonnet-4-5", body.Entries[0].ModelID)
	assert.Equal(t, providers.ProviderAnthropic, body.Entries[0].Provider)
	assert.Equal(t, "online", body.Entries[0].Status)
}

func TestUpdateModelStatusHandlerPinsAndResets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := modelStatusFixture()
	router := gin.New()
	router.PUT("/model-status", admin.UpdateModelStatusHandler(store))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/model-status", bytes.NewBufferString(`{"model_id":"claude-sonnet-4-5","provider":"anthropic","status":"maintenance","reason":"window"}`)))
	require.Equal(t, http.StatusOK, response.Code)
	entry, ok := store.Get(context.Background(), modelstatus.Key{ModelID: "claude-sonnet-4-5", Provider: providers.ProviderAnthropic})
	require.True(t, ok)
	assert.Equal(t, modelstatus.StatusMaintenance, entry.Status)
	assert.True(t, entry.AdminPinned)

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/model-status", bytes.NewBufferString(`{"model_id":"claude-sonnet-4-5","provider":"anthropic","status":"auto"}`)))
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, modelstatus.StatusOnline, store.Lookup(context.Background(), entry.Key))
}

func TestProviderInventoryIncludesProvidersWithoutBindings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/provider-inventory", admin.ProviderInventoryHandler(modelStatusFixture()))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/provider-inventory", nil))

	require.Equal(t, http.StatusOK, response.Code)
	var body struct {
		Providers []struct {
			Provider string `json:"provider"`
		} `json:"providers"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Len(t, body.Providers, len(providers.AllProviders()))
}
