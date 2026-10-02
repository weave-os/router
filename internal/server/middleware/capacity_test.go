package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/health"
	"weave-os/router/internal/server/middleware"
)

func TestCapacityMiddlewareKeepsProbesResponsiveWhileWorkIsHeld(t *testing.T) {
	gin.SetMode(gin.TestMode)
	capacity, err := health.NewCapacity(health.Limits{MaxRequests: 1})
	require.NoError(t, err)
	engine := gin.New()
	engine.Use(middleware.WithCapacity(capacity))
	engine.GET("/health", admin.HealthHandler)
	engine.GET("/startupz", admin.StartupHandler(func() bool { return true }))
	engine.GET("/capacityz", admin.CapacityHandler(capacity))
	engine.GET("/readyz", admin.ReadinessHandler(nil))
	started, finish, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	engine.POST("/v1/messages", func(c *gin.Context) {
		close(started)
		<-finish
		c.Status(http.StatusOK)
	})
	first := httptest.NewRecorder()
	go func() {
		defer close(finished)
		engine.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	}()
	<-started
	for _, test := range []struct {
		path   string
		status int
	}{
		{"/health", http.StatusOK},
		{"/startupz", http.StatusOK},
		{"/readyz", http.StatusOK},
		{"/capacityz", http.StatusServiceUnavailable},
	} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		assert.Equal(t, test.status, response.Code, test.path)
	}
	rejected := httptest.NewRecorder()
	engine.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rejected.Code)
	assert.Equal(t, "1", rejected.Header().Get("Retry-After"))
	assert.Contains(t, rejected.Body.String(), `"type":"error"`)
	close(finish)
	<-finished
	assert.Equal(t, http.StatusOK, first.Code)
	assert.True(t, capacity.Snapshot().Ready)
	assert.Zero(t, capacity.Snapshot().ActiveRequests)
}

func TestCapacityMiddlewareReleasesAfterRecoveredPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	capacity, err := health.NewCapacity(health.Limits{MaxRequests: 1})
	require.NoError(t, err)
	engine := gin.New()
	engine.Use(middleware.WithCapacity(capacity), gin.Recovery())
	engine.GET("/panic", func(*gin.Context) { panic("synthetic serving failure") })
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.True(t, capacity.Snapshot().Ready)
	assert.Zero(t, capacity.Snapshot().ActiveRequests)
}
