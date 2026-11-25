package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/analytics"
	"weave-os/router/internal/server"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type emptyUsageRepo struct{}

func (emptyUsageRepo) GetUsageRows(context.Context, analytics.UsageQuery) ([]analytics.UsageRow, error) {
	return nil, nil
}

const usageTestToken = "internal-usage-token"

func registerUsageEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	server.RegisterWithFeatures(engine, nil, server.Services{}, nil, server.DeploymentModeManaged, nil, nil, nil, analytics.NewService(nil, nil), server.Features{
		InternalServiceToken: usageTestToken,
		UsageAnalytics:       analytics.NewUsageService(emptyUsageRepo{}, nil),
	})
	return engine
}

func TestRegisterMountsOnlyUsageUnderInternalAnalytics(t *testing.T) {
	got := routeSet(registerUsageEngine(t))
	assert.Contains(t, got, "GET /internal/v1/analytics/usage")
	for _, deferred := range []string{
		"GET /internal/v1/analytics/routing-decisions",
		"GET /internal/v1/analytics/models",
		"GET /internal/v1/analytics/schema",
	} {
		assert.NotContains(t, got, deferred, "the export move is deferred to the analytics-key removal")
	}
}

func TestInternalUsageRequiresServiceToken(t *testing.T) {
	engine := registerUsageEngine(t)
	path := "/internal/v1/analytics/usage?organization_id=org-1&start=2026-11-01&end=2026-11-30"

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	req.Header.Set("X-Weave-Internal-Token", usageTestToken)
	engine.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"rows":[],"next_cursor":null}`, rec.Body.String())
}

func TestPublicAnalyticsRoutesCarryDeprecationHeader(t *testing.T) {
	engine := registerUsageEngine(t)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/analytics/schema", nil))
	assert.Equal(t, "@1796083200", rec.Header().Get("Deprecation"))
}
