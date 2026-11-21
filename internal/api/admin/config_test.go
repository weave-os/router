package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfigHandler_EnvProviderKeys_IncludesEveryDeployedProvider guards
// [114]: ConfigHandler used to derive its display list from a hand-maintained
// literal that silently omitted providers added after the initial list even
// though they're wired into providerMap, so an operator who set the env var
// still saw the key reported as absent. ConfigHandler must now report every
// provider whose env var is actually set, regardless of when the provider
// was added to internal/providers.
func TestConfigHandler_EnvProviderKeys_IncludesEveryDeployedProvider(t *testing.T) {
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderMakora), "dummy-key")
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderBedrock), "dummy-key")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/admin/v1/config", func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: "inst-1"})
	}, admin.ConfigHandler(admin.Config{HasDeploymentProviderKey: func(p string) bool { return os.Getenv(providers.APIKeyEnvVar(p)) != "" }}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/v1/config", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		EnvProviderKeys []string `json:"env_provider_keys"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Contains(t, body.EnvProviderKeys, providers.ProviderMakora)
	require.Contains(t, body.EnvProviderKeys, providers.ProviderBedrock)
	require.NotContains(t, rec.Body.String(), "cluster_version")
}

func TestConfigHandlerZeroConfigReportsDefaultStickyTTL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/admin/v1/config", func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: "inst-1"})
	}, admin.ConfigHandler(admin.Config{}))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/admin/v1/config", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"sticky_decision_ttl_ms":"0","otel_enabled":false,"env_provider_keys":[]}`, rec.Body.String())
}
