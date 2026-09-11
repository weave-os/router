package middleware

import (
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"

	"github.com/gin-gonic/gin"
)

// ToolCheckOverrideHeader is the request-header key for x-weave-toolcheck.
const ToolCheckOverrideHeader = "x-weave-toolcheck"

// WithToolCheckOverride parses x-weave-toolcheck (off | standard | semantic)
// and stashes the mode on the request context. Invalid values abort with 400;
// an absent header leaves the deployment default.
func WithToolCheckOverride() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := strings.TrimSpace(c.GetHeader(ToolCheckOverrideHeader))
		if raw == "" {
			c.Next()
			return
		}
		mode, ok := router.ParseToolCheckMode(raw)
		if !ok {
			abortInvalidKnob(c, ToolCheckOverrideHeader+" must be one of: off, standard, semantic.")
			return
		}
		c.Request = c.Request.WithContext(router.WithToolCheckMode(c.Request.Context(), mode))
		observability.FromGin(c).Debug("Toolcheck override applied", "override_toolcheck", string(mode))
		c.Next()
	}
}
