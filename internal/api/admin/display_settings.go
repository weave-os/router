package admin

import (
	"net/http"

	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// displaySettingsResponse is the per-installation toggle set read by the installer and statusline.
type displaySettingsResponse struct {
	HideTerminalSurfaces bool `json:"hide_terminal_surfaces"`
}

// DisplaySettingsHandler returns the calling installation's display toggles (scoped to the caller by the rk_ bearer key).
func DisplaySettingsHandler(c *gin.Context) {
	installation := middleware.InstallationFrom(c)
	if installation == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
		return
	}
	c.JSON(http.StatusOK, displaySettingsResponse{
		HideTerminalSurfaces: installation.HideTerminalSurfaces,
	})
}
