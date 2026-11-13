package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// replicaEngine is one worker replica's cost route stack: its limiter state is
// private to the engine, as it is to a process.
func replicaEngine(svc *auth.Service, now func() time.Time) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/v1/sessions/:session_id/cost",
		middleware.WithReadKey(svc),
		middleware.WithInstallationRateLimit(middleware.SessionCostRequestsPerMinute, now),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)
	return engine
}

func costRequest(engine *gin.Engine, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/session-1/cost", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestInstallationRateLimitSharesOneBucketAcrossKeyTypes(t *testing.T) {
	installation := &auth.Installation{ID: "inst-cost", ExternalID: "ext-cost"}
	svc := readKeyService(t, installation, map[string]auth.APIKeyScope{
		"rk_routing":   auth.ScopeRouting,
		"ra_analytics": auth.ScopeAnalyticsRead,
	})
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return start }
	replica := replicaEngine(svc, clock)

	tokens := []string{"rk_routing", "ra_analytics"}
	for i := range middleware.SessionCostRequestsPerMinute {
		rec := costRequest(replica, tokens[i%2])
		require.Equal(t, http.StatusOK, rec.Code, "request %d is inside the burst", i+1)
		require.Equal(t, strconv.Itoa(middleware.SessionCostRequestsPerMinute-i-1), rec.Header().Get("X-RateLimit-Remaining"))
		require.Equal(t, strconv.Itoa(i+1), rec.Header().Get("X-RateLimit-Used"))
	}
	first := costRequest(replicaEngine(svc, clock), "rk_routing")
	assert.Equal(t, "500", first.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, strconv.FormatInt(start.Add(120*time.Millisecond).Unix(), 10), first.Header().Get("X-RateLimit-Reset"))

	for _, token := range tokens {
		rec := costRequest(replica, token)
		require.Equal(t, http.StatusTooManyRequests, rec.Code, "the other key type must not get a fresh bucket")
		assert.Equal(t, "1", rec.Header().Get("Retry-After"))
		assert.Equal(t, "500", rec.Header().Get("X-RateLimit-Limit"))
		assert.Equal(t, "0", rec.Header().Get("X-RateLimit-Remaining"))
		assert.Equal(t, "500", rec.Header().Get("X-RateLimit-Used"))
		assert.Equal(t, strconv.FormatInt(start.Add(time.Minute).Unix(), 10), rec.Header().Get("X-RateLimit-Reset"))
		var body struct {
			Message     string `json:"message"`
			Description string `json:"description"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, "Too many requests", body.Message)
		assert.Equal(t, "Rate limit exceeded. Please retry after 120ms", body.Description)
	}

	secondReplica := replicaEngine(svc, clock)
	assert.Equal(t, http.StatusOK, costRequest(secondReplica, "ra_analytics").Code, "each replica holds its own bucket")
}

func TestInstallationRateLimitRefillsOverTime(t *testing.T) {
	installation := &auth.Installation{ID: "inst-cost", ExternalID: "ext-cost"}
	svc := readKeyService(t, installation, map[string]auth.APIKeyScope{"rk_routing": auth.ScopeRouting})
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	engine := replicaEngine(svc, func() time.Time { return now })
	for range middleware.SessionCostRequestsPerMinute {
		require.Equal(t, http.StatusOK, costRequest(engine, "rk_routing").Code)
	}
	require.Equal(t, http.StatusTooManyRequests, costRequest(engine, "rk_routing").Code)

	now = now.Add(time.Minute / middleware.SessionCostRequestsPerMinute)

	assert.Equal(t, http.StatusOK, costRequest(engine, "rk_routing").Code)
	assert.Equal(t, http.StatusTooManyRequests, costRequest(engine, "rk_routing").Code)
}

func TestInstallationRateLimitIsPerInstallation(t *testing.T) {
	first := &auth.Installation{ID: "inst-a"}
	second := &auth.Installation{ID: "inst-b"}
	engine := gin.New()
	engine.GET("/x", func(c *gin.Context) {
		if c.GetHeader("X-Installation") == second.ID {
			c.Set("router_installation", second)
		} else {
			c.Set("router_installation", first)
		}
	}, middleware.WithInstallationRateLimit(1, time.Now), func(c *gin.Context) { c.Status(http.StatusOK) })
	call := func(installationID string) int {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("X-Installation", installationID)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusOK, call(first.ID))
	require.Equal(t, http.StatusTooManyRequests, call(first.ID))
	assert.Equal(t, http.StatusOK, call(second.ID))
}
