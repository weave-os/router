package admin

import (
	"context"
	"net/http"

	"weave-os/router/internal/health"
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

// StartupHandler observes the boot-owned completion latch without rerunning work.
func StartupHandler(started func() bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if started == nil || !started() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "initializing"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

func CapacityHandler(capacity *health.Capacity) gin.HandlerFunc {
	if capacity == nil {
		return func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "capacity_not_configured"})
		}
	}
	probe := capacity.Handler()
	return func(c *gin.Context) { probe.ServeHTTP(c.Writer, c.Request) }
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
