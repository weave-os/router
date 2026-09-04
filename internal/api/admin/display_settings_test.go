package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestDisplaySettingsHandler covers the flag passthrough and unauthenticated rejection.
func TestDisplaySettingsHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("reports the installation flag", func(t *testing.T) {
		for _, hide := range []bool{true, false} {
			engine := gin.New()
			engine.GET("/v1/display-settings", func(c *gin.Context) {
				c.Set("router_installation", &auth.Installation{ID: "inst-1", HideTerminalSurfaces: hide})
			}, admin.DisplaySettingsHandler)

			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

			require.Equal(t, http.StatusOK, rec.Code)
			var body struct {
				HideTerminalSurfaces bool `json:"hide_terminal_surfaces"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, hide, body.HideTerminalSurfaces)
		}
	})

	t.Run("unauthenticated without an installation", func(t *testing.T) {
		engine := gin.New()
		engine.GET("/v1/display-settings", admin.DisplaySettingsHandler)

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}
