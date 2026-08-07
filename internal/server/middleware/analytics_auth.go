package middleware

import (
	"github.com/gin-gonic/gin"

	"workweave/router/internal/auth"
)

// WithAnalyticsKey validates the inbound request via a bearer ra_ token and
// admits only analytics-scoped keys. Used on the read-only export surface. On
// failure, short-circuits 401.
//
// Unlike WithAuth it stashes nothing on the request context: the export reads
// telemetry for the authed installation and has no routing, BYOK, or billing
// decisions downstream of it.
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
