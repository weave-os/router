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
	"workweave/router/internal/auth"
	"workweave/router/internal/providers"
	"workweave/router/internal/router/cluster"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fenceInstallationRepository records the last fence write so tests can assert
// the handler scopes it to the caller's own installation.
type fenceInstallationRepository struct {
	auth.InstallationRepository
	gotExternalID string
	gotID         string
	gotProviders  []string
	writes        int
}

func (f *fenceInstallationRepository) UpdateAllowedProviders(_ context.Context, externalID, id string, providerNames []string) error {
	f.writes++
	f.gotExternalID, f.gotID, f.gotProviders = externalID, id, providerNames
	return nil
}

// fenceOverride is a ProviderFenceOverrideSource standing in for a deployment
// that pinned ROUTER_ALLOWED_PROVIDERS.
type fenceOverride struct {
	allowed []string
}

func (f fenceOverride) HasAllowedProvidersOverride() bool { return len(f.allowed) > 0 }
func (f fenceOverride) AllowedProvidersOverride() []string {
	return f.allowed
}

func fenceEngine(svc *auth.Service, installation *auth.Installation, override admin.ProviderFenceOverrideSource) *gin.Engine {
	gin.SetMode(gin.TestMode)
	models := fakeDeployedModels{entries: []cluster.DeployedEntry{
		{Model: "claude-opus-4-7", Provider: providers.ProviderAnthropic},
		{Model: "claude-haiku-4-5", Provider: providers.ProviderAnthropic},
		{Model: "gpt-5.5", Provider: providers.ProviderOpenAI},
	}}
	engine := gin.New()
	inject := func(c *gin.Context) { c.Set("router_installation", installation) }
	engine.GET("/admin/v1/allowed-providers", inject, admin.GetAllowedProvidersHandler(svc, models, override))
	engine.PUT("/admin/v1/allowed-providers", inject, admin.UpdateAllowedProvidersHandler(svc, models, override))
	return engine
}

func fenceService(repo auth.InstallationRepository) *auth.Service {
	return auth.NewService(repo, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Unix(0, 0) })
}

func putFence(t *testing.T, engine *gin.Engine, allowed []string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string][]string{"allowed": allowed})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/admin/v1/allowed-providers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func decodeFence(t *testing.T, rec *httptest.ResponseRecorder) struct {
	Available         []string `json:"available"`
	Allowed           []string `json:"allowed"`
	EnvOverrideActive bool     `json:"env_override_active"`
} {
	t.Helper()
	var body struct {
		Available         []string `json:"available"`
		Allowed           []string `json:"allowed"`
		EnvOverrideActive bool     `json:"env_override_active"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

func TestGetAllowedProvidersHandler(t *testing.T) {
	t.Run("reports the stored fence alongside what's deployed", func(t *testing.T) {
		installation := &auth.Installation{ID: "inst-1", ExternalID: "ext-1", AllowedProviders: []string{providers.ProviderAnthropic}}
		engine := fenceEngine(fenceService(&fenceInstallationRepository{}), installation, nil)

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/allowed-providers", nil))

		require.Equal(t, http.StatusOK, rec.Code)
		body := decodeFence(t, rec)
		assert.Equal(t, []string{providers.ProviderAnthropic}, body.Allowed)
		assert.Equal(t, []string{providers.ProviderAnthropic, providers.ProviderOpenAI}, body.Available,
			"the picker needs the deployed set, deduplicated across models")
		assert.False(t, body.EnvOverrideActive)
	})

	// The UI reads an empty list as "unfenced", so it must never serialize as
	// JSON null — that would be indistinguishable from a fence of nothing.
	t.Run("an unfenced installation returns an empty list", func(t *testing.T) {
		engine := fenceEngine(fenceService(&fenceInstallationRepository{}), &auth.Installation{ID: "inst-1"}, nil)

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/allowed-providers", nil))

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"allowed":[]`)
	})

	t.Run("a pinned deployment reports its own fence, not the installation's", func(t *testing.T) {
		installation := &auth.Installation{ID: "inst-1", AllowedProviders: []string{providers.ProviderOpenAI}}
		engine := fenceEngine(fenceService(&fenceInstallationRepository{}), installation,
			fenceOverride{allowed: []string{providers.ProviderAnthropic}})

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/allowed-providers", nil))

		body := decodeFence(t, rec)
		assert.Equal(t, []string{providers.ProviderAnthropic}, body.Allowed,
			"the env override is what actually constrains egress, so it's what the UI must show")
		assert.True(t, body.EnvOverrideActive, "the UI renders read-only off this flag")
	})
}

func TestUpdateAllowedProvidersHandler(t *testing.T) {
	t.Run("persists the fence scoped to the caller's installation", func(t *testing.T) {
		repo := &fenceInstallationRepository{}
		installation := &auth.Installation{ID: "inst-1", ExternalID: "ext-1"}
		engine := fenceEngine(fenceService(repo), installation, nil)

		rec := putFence(t, engine, []string{providers.ProviderOpenAI, providers.ProviderAnthropic})

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "ext-1", repo.gotExternalID, "the write must be tenant-scoped, not by installation id alone")
		assert.Equal(t, "inst-1", repo.gotID)
		assert.Equal(t, []string{providers.ProviderOpenAI, providers.ProviderAnthropic}, repo.gotProviders)
		assert.Equal(t, []string{providers.ProviderAnthropic, providers.ProviderOpenAI}, decodeFence(t, rec).Allowed,
			"the response is sorted so the UI doesn't reorder on every save")
	})

	t.Run("an empty list removes the fence", func(t *testing.T) {
		repo := &fenceInstallationRepository{}
		engine := fenceEngine(fenceService(repo), &auth.Installation{ID: "inst-1"}, nil)

		rec := putFence(t, engine, []string{})

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, 1, repo.writes)
		assert.Empty(t, repo.gotProviders)
	})

	// A typo'd provider would fence the installation down to nothing, taking it
	// offline — reject it instead of persisting it.
	t.Run("a provider this deployment doesn't run is rejected", func(t *testing.T) {
		repo := &fenceInstallationRepository{}
		engine := fenceEngine(fenceService(repo), &auth.Installation{ID: "inst-1"}, nil)

		rec := putFence(t, engine, []string{providers.ProviderAnthropic, "anthropc"})

		require.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Zero(t, repo.writes, "a rejected request must not persist a partial fence")
	})

	t.Run("a pinned deployment refuses edits", func(t *testing.T) {
		repo := &fenceInstallationRepository{}
		engine := fenceEngine(fenceService(repo), &auth.Installation{ID: "inst-1"},
			fenceOverride{allowed: []string{providers.ProviderAnthropic}})

		rec := putFence(t, engine, []string{providers.ProviderOpenAI})

		require.Equal(t, http.StatusForbidden, rec.Code)
		assert.Zero(t, repo.writes, "the env override is the operator's boundary; the API must not appear to widen it")
	})
}

// resolveInstallation returns 401 rather than falling through to an unscoped
// write when no principal is attached.
func TestAllowedProvidersHandlers_RequireAnInstallation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &fenceInstallationRepository{}
	svc := fenceService(repo)
	models := fakeDeployedModels{}
	engine := gin.New()
	engine.PUT("/admin/v1/allowed-providers", admin.UpdateAllowedProvidersHandler(svc, models, nil))

	rec := putFence(t, engine, []string{providers.ProviderAnthropic})

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Zero(t, repo.writes)
}
