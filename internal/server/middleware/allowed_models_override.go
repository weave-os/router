package middleware

import (
	"context"
	"net/http"
	"strings"

	"workweave/router/internal/observability"
	"workweave/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

// AllowedModelsHeaderGate resolves the org-overridable allowed_models_header
// flag; *proxy.Service implements it.
type AllowedModelsHeaderGate interface {
	ResolveAllowedModelsHeader(ctx context.Context) bool
}

// WithAllowedModelsOverride narrows routing to the x-weave-allowed-models
// subset for installations authorized for policy headers or organizations
// with the allowed_models_header flag on. The subset is intersected with the
// installation allowlist; an unknown alias or an empty intersection is a 400,
// and an unauthorized caller is a 403 — never a silent fall back to the full
// roster.
func WithAllowedModelsOverride(gate AllowedModelsHeaderGate) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := strings.TrimSpace(c.GetHeader(proxy.AllowedModelsHeader))
		if raw == "" {
			c.Next()
			return
		}
		installation := InstallationFrom(c)
		if installation == nil {
			c.Next()
			return
		}
		ctx := c.Request.Context()
		if !installation.PolicyHeaderOverridesEnabled && !gate.ResolveAllowedModelsHeader(ctx) {
			observability.FromGin(c).Warn("Allowed-models override rejected: installation is not authorized for policy headers", "installation_id", installation.ID)
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "allowed_models_header_not_authorized"})
			return
		}
		subset, err := proxy.ParseAllowedModelsHeader(raw, installation.AllowedModels)
		if err != nil {
			observability.FromGin(c).Warn("Allowed-models override rejected", "installation_id", installation.ID, "err", err)
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "allowed_models_header_invalid", "message": err.Error()})
			return
		}
		c.Request = c.Request.WithContext(context.WithValue(ctx, proxy.RequestAllowedModelsContextKey{}, subset))
		c.Next()
	}
}
