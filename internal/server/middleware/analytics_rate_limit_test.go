package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func rateLimitedEngine(perMinute int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/x", func(c *gin.Context) {
		if id := c.GetHeader("X-Test-Key-ID"); id != "" {
			c.Set("router_api_key", &auth.APIKey{ID: id})
		}
	}, middleware.WithAnalyticsRateLimit(perMinute), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	return engine
}

func callAs(engine *gin.Engine, keyID string) int {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if keyID != "" {
		req.Header.Set("X-Test-Key-ID", keyID)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec.Code
}

func TestAnalyticsRateLimitAllowsBurstThenRejects(t *testing.T) {
	engine := rateLimitedEngine(3)

	for i := range 3 {
		require.Equal(t, http.StatusOK, callAs(engine, "key-1"), "request %d should be inside the burst", i)
	}

	require.Equal(t, http.StatusTooManyRequests, callAs(engine, "key-1"))
}

// One noisy ETL job must not throttle another installation's export.
func TestAnalyticsRateLimitIsPerKey(t *testing.T) {
	engine := rateLimitedEngine(1)
	require.Equal(t, http.StatusOK, callAs(engine, "key-1"))
	require.Equal(t, http.StatusTooManyRequests, callAs(engine, "key-1"))

	require.Equal(t, http.StatusOK, callAs(engine, "key-2"))
}

func TestAnalyticsRateLimitSetsRetryAfter(t *testing.T) {
	engine := rateLimitedEngine(1)
	require.Equal(t, http.StatusOK, callAs(engine, "key-1"))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Test-Key-ID", "key-1")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
}

// Auth runs first and rejects anonymous callers; the limiter must not panic or
// lump them into a shared bucket if it ever sees one.
func TestAnalyticsRateLimitIgnoresUnauthenticated(t *testing.T) {
	engine := rateLimitedEngine(1)

	require.Equal(t, http.StatusOK, callAs(engine, ""))
	require.Equal(t, http.StatusOK, callAs(engine, ""))
}
