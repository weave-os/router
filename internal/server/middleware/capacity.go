package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/health"
)

// WithCapacity keeps accepted work accounted until the handler and any stream
// finish. Probes never contend for a serving permit.
func WithCapacity(capacity *health.Capacity) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			switch c.Request.URL.Path {
			case "/health", "/startupz", "/capacityz", "/readyz":
				c.Next()
				return
			}
		}
		permit := capacity.TryAcquire()
		if permit == nil {
			c.Header("Retry-After", "1")
			envelope := gin.H{"error": gin.H{"type": "api_error", "message": "Router instance is at capacity; retry later."}}
			if strings.HasPrefix(c.Request.URL.Path, "/v1beta/") {
				envelope = gin.H{"error": gin.H{"code": http.StatusServiceUnavailable, "status": "UNAVAILABLE", "message": "Router instance is at capacity; retry later."}}
			} else if c.Request.URL.Path == "/v1/messages" || c.Request.URL.Path == "/v1/messages/count_tokens" {
				envelope["type"] = "error"
			}
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, envelope)
			return
		}
		defer permit.Release()
		c.Next()
	}
}
