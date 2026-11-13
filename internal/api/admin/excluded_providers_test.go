package admin_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func excludedProvidersEngine(models admin.HMMRosterSource) *gin.Engine {
	gin.SetMode(gin.TestMode)
	svc := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Unix(0, 0) })
	engine := gin.New()
	withInstallation := func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: "inst-1", ExcludedProviders: []string{providers.ProviderOpenAI}})
	}
	engine.GET("/admin/v1/excluded-providers", withInstallation, admin.GetExcludedProvidersHandler(svc, models, nil))
	engine.PUT("/admin/v1/excluded-providers", withInstallation, admin.UpdateExcludedProvidersHandler(svc, models, nil))
	return engine
}

func hmmProviderRoster() *fakeHMMRoster {
	return &fakeHMMRoster{entries: []router.DeployedEntry{
		{Model: "gpt-5.4-mini", Provider: providers.ProviderOpenAI},
		{Model: "claude-opus-5", Provider: providers.ProviderAnthropic},
		{Model: "claude-haiku-4-5", Provider: providers.ProviderAnthropic},
	}}
}

func TestGetExcludedProvidersHandler_AvailableComesFromHMMRoster(t *testing.T) {
	rec := httptest.NewRecorder()
	excludedProvidersEngine(hmmProviderRoster()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/excluded-providers", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Available []string `json:"available"`
		Excluded  []string `json:"excluded"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, []string{providers.ProviderAnthropic, providers.ProviderOpenAI}, body.Available)
	assert.Equal(t, []string{providers.ProviderOpenAI}, body.Excluded)
}

func TestUpdateExcludedProvidersHandler_RejectsProviderOutsideHMMRoster(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"excluded": []string{providers.ProviderFireworks}})
	req := httptest.NewRequest(http.MethodPut, "/admin/v1/excluded-providers", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	excludedProvidersEngine(hmmProviderRoster()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "a provider the serving roster never routes to is not a valid exclusion")
}

func TestExcludedProvidersHandlers_RosterUnavailableIs503(t *testing.T) {
	engine := excludedProvidersEngine(&fakeHMMRoster{err: errors.New("policy not loaded")})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/excluded-providers", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	payload, _ := json.Marshal(map[string]any{"excluded": []string{providers.ProviderOpenAI}})
	req := httptest.NewRequest(http.MethodPut, "/admin/v1/excluded-providers", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
