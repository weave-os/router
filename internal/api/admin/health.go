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

// HealthHandler is the process responsive check; it does not contact dependencies.
func HealthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// StartupHandler requires database connectivity before the instance receives traffic.
func StartupHandler(ping func(context.Context) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		if ping == nil {
			observability.FromGin(c).Error("Startup database check is not configured")
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
			return
		}
		if err := ping(c.Request.Context()); err != nil {
			observability.FromGin(c).Warn("Startup database check failed", "err", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

// ReadinessHandler reports whether optional dependencies are ready.
func ReadinessHandler(checker HealthChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
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
