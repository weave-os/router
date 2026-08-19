package admin

import (
	"net/http"

	"workweave/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// displaySettingsResponse is the client-facing view of the per-installation
// terminal-surface display toggles. The installer and statusline read it with
// the data-plane rk_ key to decide whether to render router surfaces
// (statusline bar today); it carries no billing or routing internals.
type displaySettingsResponse struct {
	HideTerminalSurfaces bool `json:"hide_terminal_surfaces"`
}

// DisplaySettingsHandler returns the calling installation's display toggles.
// Bearer rk_ auth only (mounted on the data plane, not the admin dashboard):
// the key identifies the org, so the response is already scoped to the caller.
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
