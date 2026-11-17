package admin

import (
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

func resolveInstallation(c *gin.Context, authSvc *auth.Service) (*auth.Installation, bool) {
	if installation := middleware.InstallationFrom(c); installation != nil {
		return installation, true
	}
	if admin := middleware.AdminPrincipalFrom(c); admin != nil {
		installation, err := authSvc.EnsureAdminInstallation(c.Request.Context())
		if err != nil {
			observability.FromGin(c).Error("Failed to ensure admin installation", "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve admin installation."})
			return nil, false
		}
		return installation, true
	}
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
	return nil, false
}
