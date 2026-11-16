package middleware

import (
	"github.com/gin-gonic/gin"

	"weave-os/router/internal/auth"
)

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
