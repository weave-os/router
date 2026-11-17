package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"weave-os/router/internal/policyclient"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cloudRunStartupProbeTimeout = 3 * time.Second

func TestStartupCheckAnswersBeforeCloudRunProbeTimeout(t *testing.T) {
	assert.Less(t, startupTimeout, cloudRunStartupProbeTimeout, "a database failure must be returned before Cloud Run abandons the startup probe")
}

func TestCatalogModelsTimeoutOutlivesPolicySidecarBudget(t *testing.T) {
	assert.Greater(t, catalogModelsTimeout, policyclient.DefaultTimeout, "a cold catalog cache waits on the HMM sidecar client budget")
}

func TestUIStaticServesCleanURLsUnderRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.html"), []byte("index"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "settings.html"), []byte("settings page"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "app.js"), []byte("script"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(root), "outside.html"), []byte("outside"), 0o600))
	engine := gin.New()
	registerUIStatic(engine, root)

	serve := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		return recorder
	}

	redirect := serve("/ui/settings/")
	assert.Equal(t, http.StatusPermanentRedirect, redirect.Code)
	assert.Equal(t, "/ui/settings", redirect.Header().Get("Location"))
	assert.Equal(t, "index", serve("/ui").Body.String())
	assert.Equal(t, "index", serve("/ui/index").Body.String())
	assert.Equal(t, "script", serve("/ui/app.js").Body.String())
	assert.Equal(t, "settings page", serve("/ui/settings").Body.String())
	assert.Equal(t, http.StatusNotFound, serve("/ui/missing").Code)
	assert.Equal(t, http.StatusNotFound, serve("/ui/../outside").Code)
	assert.Equal(t, http.StatusNotFound, serve("/ui/%2e%2e/outside").Code)
}
