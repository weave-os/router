package admin

import (
	"context"
	"net/http"

	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
)

// HealthChecker reports whether a dependency is ready to serve traffic.
type HealthChecker interface {
	CheckHealth(ctx context.Context) error
}

// HealthHandler reports process liveness without checking optional dependencies.
func HealthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ReadinessHandler reports whether optional dependencies are ready.
func ReadinessHandler(checker HealthChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		if readinessChecker, ok := checker.(interface {
			CheckReadiness(context.Context) (bool, error)
		}); ok {
			degraded, err := readinessChecker.CheckReadiness(c.Request.Context())
			if err != nil {
				observability.FromGin(c).Debug("Readiness check failed", "err", err)
				c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
			} else if degraded {
				observability.FromGin(c).Warn("Readiness is degraded", "primary_routing", "unavailable", "serving_recovery", true)
				c.JSON(http.StatusOK, gin.H{"status": "degraded", "primary_routing": "unavailable", "serving_recovery": true})
			} else {
				c.JSON(http.StatusOK, gin.H{"status": "ok", "primary_routing": "healthy"})
			}
			return
		}
		if checker != nil {
			if err := checker.CheckHealth(c.Request.Context()); err != nil {
				observability.FromGin(c).Debug("Readiness check failed", "err", err)
				c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}
