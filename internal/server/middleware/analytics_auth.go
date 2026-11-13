package middleware

import (
	"github.com/gin-gonic/gin"

	"weave-os/router/internal/auth"
)

// WithAnalyticsKey authenticates ra_ bearer tokens for the read-only export surface;
// rejects all other key types with 401. Stashes installation and key on the gin
// context only — no routing, BYOK, or billing decisions follow.
func WithAnalyticsKey(svc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, apiKey, err := svc.VerifyAnalyticsAPIKey(c.Request.Context(), extractToken(c))
		if err != nil {
			handleAuthError(c, err)
			return
		}
		c.Set(ctxKeyInstallation, installation)
		c.Set(ctxKeyAPIKey, apiKey)
		c.Next()
	}
}

// WithReadKey authenticates an rk_ routing or ra_ analytics bearer token for
// installation-scoped read surfaces, stashing the owning installation and key.
// No serving admission, routing policy, BYOK, subscription or billing state
// follows, so neither key type gains spend authority here.
func WithReadKey(svc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, apiKey, err := svc.VerifyReadAPIKey(c.Request.Context(), extractToken(c))
		if err != nil {
			handleAuthError(c, err)
			return
		}
		c.Set(ctxKeyInstallation, installation)
		c.Set(ctxKeyAPIKey, apiKey)
		c.Next()
	}
}
