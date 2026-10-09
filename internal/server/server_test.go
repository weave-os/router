package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"weave-os/router/internal/analytics"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDeployedModelsSource is a stand-in for *cluster.Multiversion in route
// registration tests; the handler closures it backs are never invoked.
type fakeDeployedModelsSource struct{}

func (fakeDeployedModelsSource) DefaultDeployedModels() []cluster.DeployedEntry { return nil }

type healthCheckerFunc func(context.Context) error

func (f healthCheckerFunc) CheckHealth(ctx context.Context) error {
	return f(ctx)
}

// routeSet collects "METHOD path" pairs so assertions are robust to additions of unrelated product routes.
func routeSet(engine *gin.Engine) map[string]struct{} {
	out := make(map[string]struct{}, len(engine.Routes()))
	for _, r := range engine.Routes() {
		out[r.Method+" "+r.Path] = struct{}{}
	}
	return out
}

func TestRegister_DeploymentMode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Product surface — always mounted regardless of deployment mode.
	productRoutes := []string{
		"GET /livez",
		"GET /health",
		"GET /readyz",
		"GET /startupz",
		"GET /validate",
		"POST /v1/client-events",
		"GET /v1/router/models",
		"POST /v1/messages",
		"POST /v1/chat/completions",
		"POST /v1/responses",
		"POST /v1/route",
		"POST /v1/route/preview",
		"POST /v1/messages/count_tokens",
		"GET /v1/models",
		"GET /v1/models/:model",
		// The Codex status hook reads this on hosted (managed) installs, so
		// mounting it inside the selfhosted block would strand every customer.
		"GET /v1/sessions/:session_id/cost",
	}

	// Self-hoster dashboard surface — gated by DeploymentModeSelfHosted.
	dashboardRoutes := []string{
		"GET /",
		"GET /ui/*filepath",
		"HEAD /ui/*filepath",
		"POST /admin/v1/auth/login",
		"POST /admin/v1/auth/logout",
		"GET /admin/v1/auth/me",
		"GET /admin/v1/metrics/summary",
		"GET /admin/v1/metrics/timeseries",
		"GET /admin/v1/keys",
		"POST /admin/v1/keys",
		"DELETE /admin/v1/keys/:id",
		"GET /admin/v1/provider-keys",
		"POST /admin/v1/provider-keys",
		"PUT /admin/v1/provider-keys/:id/model-aliases",
		"DELETE /admin/v1/provider-keys/:id",
		"GET /admin/v1/config",
		"GET /admin/v1/excluded-models",
		"PUT /admin/v1/excluded-models",
	}

	t.Run("selfhosted mounts dashboard and product routes", func(t *testing.T) {
		engine := gin.New()
		// Nil services are fine: engine.Routes() inspection never invokes the closure-captured handlers.
		server.Register(engine, nil, nil, fakeDeployedModelsSource{}, nil, server.DeploymentModeSelfHosted, nil, nil, nil, nil)
		got := routeSet(engine)
		for _, want := range productRoutes {
			assert.Contains(t, got, want, "product route missing in selfhosted mode")
		}
		for _, want := range dashboardRoutes {
			assert.Contains(t, got, want, "dashboard route missing in selfhosted mode")
		}
	})

	t.Run("managed skips dashboard but keeps product routes", func(t *testing.T) {
		engine := gin.New()
		// Pass a non-nil DeployedModelsSource: managed prod always boots a
		// *cluster.Multiversion router, so the catalog endpoint must mount
		// even though the dashboard does not.
		server.Register(engine, nil, nil, fakeDeployedModelsSource{}, nil, server.DeploymentModeManaged, nil, nil, nil, nil)
		got := routeSet(engine)
		for _, want := range productRoutes {
			assert.Contains(t, got, want, "product route missing in managed mode")
		}
		for _, unwanted := range dashboardRoutes {
			assert.NotContains(t, got, unwanted, "dashboard route must not be mounted in managed mode")
		}
	})

	t.Run("nil deployed-models source skips catalog endpoint", func(t *testing.T) {
		engine := gin.New()
		server.Register(engine, nil, nil, nil, nil, server.DeploymentModeManaged, nil, nil, nil, nil)
		got := routeSet(engine)
		assert.NotContains(t, got, "GET /v1/router/models", "catalog endpoint must not mount without a deployed-models source")
	})
}

// The export is a product surface, so it must reach managed installations too
// — mounting it inside the selfhosted block would strand every managed customer.
func TestRegisterMountsAnalyticsExportInBothModes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	analyticsRoutes := []string{
		"GET /v1/analytics/routing-decisions",
		"GET /v1/analytics/models",
		"GET /v1/analytics/schema",
	}

	for _, mode := range []server.DeploymentMode{server.DeploymentModeSelfHosted, server.DeploymentModeManaged} {
		t.Run(string(mode), func(t *testing.T) {
			engine := gin.New()
			server.Register(engine, nil, nil, nil, nil, mode, nil, nil, nil, analytics.NewService(nil, nil))
			got := routeSet(engine)
			for _, want := range analyticsRoutes {
				assert.Contains(t, got, want)
			}
		})
	}

	t.Run("nil service leaves the surface unmounted", func(t *testing.T) {
		engine := gin.New()
		server.Register(engine, nil, nil, nil, nil, server.DeploymentModeSelfHosted, nil, nil, nil, nil)
		got := routeSet(engine)
		for _, unwanted := range analyticsRoutes {
			assert.NotContains(t, got, unwanted)
		}
	})
}

func TestRegisterSeparatesResponsiveAndStartupChecksFromReadiness(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	checker := healthCheckerFunc(func(context.Context) error {
		return errors.New("dependency unavailable")
	})
	server.RegisterWithFeatures(engine, nil, nil, nil, nil, server.DeploymentModeManaged, nil, checker, nil, nil, server.Features{
		StartupDatabasePing: func(context.Context) error { return nil },
	})

	for _, test := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/livez", wantStatus: http.StatusOK},
		{path: "/health", wantStatus: http.StatusOK},
		{path: "/readyz", wantStatus: http.StatusServiceUnavailable},
		{path: "/startupz", wantStatus: http.StatusOK},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			assert.Equal(t, test.wantStatus, response.Code)
		})
	}
}

func TestStartupCheckRequiresDatabaseAndRecovers(t *testing.T) {
	for _, mode := range []server.DeploymentMode{server.DeploymentModeManaged, server.DeploymentModeSelfHosted} {
		t.Run(string(mode), func(t *testing.T) {
			engine := gin.New()
			var databaseAvailable atomic.Bool
			server.RegisterWithFeatures(engine, nil, nil, nil, nil, mode, nil, nil, nil, nil, server.Features{
				StartupDatabasePing: func(context.Context) error {
					if !databaseAvailable.Load() {
						return errors.New("postgres internal-host unavailable")
					}
					return nil
				},
			})
			for _, available := range []bool{false, true, false} {
				databaseAvailable.Store(available)
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/startupz", nil))
				if available {
					assert.Equal(t, http.StatusOK, response.Code)
				} else {
					assert.Equal(t, http.StatusServiceUnavailable, response.Code)
					assert.NotContains(t, response.Body.String(), "internal-host")
				}
			}
		})
	}
}

func TestStartupCheckRequiresDatabaseWiring(t *testing.T) {
	engine := gin.New()
	server.Register(engine, nil, nil, nil, nil, server.DeploymentModeManaged, nil, nil, nil, nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/startupz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func TestStartupDatabaseTimeoutDoesNotBlockResponsiveChecks(t *testing.T) {
	engine := gin.New()
	started := make(chan context.Context, 1)
	server.RegisterWithFeatures(engine, nil, nil, nil, nil, server.DeploymentModeManaged, nil, nil, nil, nil, server.Features{
		StartupDatabasePing: func(ctx context.Context) error {
			started <- ctx
			<-ctx.Done()
			return ctx.Err()
		},
	})
	response := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/startupz", nil))
	}()
	select {
	case ctx := <-started:
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), 2*time.Second)
	case <-time.After(time.Second):
		t.Fatal("Startup check never attempted database connection")
	}
	for _, path := range []string{"/livez", "/health"} {
		probe := httptest.NewRecorder()
		engine.ServeHTTP(probe, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusOK, probe.Code)
	}
	select {
	case <-finished:
		assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	case <-time.After(3 * time.Second):
		t.Fatal("Startup database check exceeded its timeout")
	}
}

func TestStartupDatabaseCheckHonorsRequestCancellation(t *testing.T) {
	engine := gin.New()
	server.RegisterWithFeatures(engine, nil, nil, nil, nil, server.DeploymentModeManaged, nil, nil, nil, nil, server.Features{
		StartupDatabasePing: func(ctx context.Context) error {
			return ctx.Err()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/startupz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func TestManagedValidationRejectsPublicCredentials(t *testing.T) {
	t.Setenv("ROUTER_INTERNAL_SERVICE_TOKEN", "internal-fixture-token")
	engine := gin.New()
	server.RegisterWithFeatures(engine, nil, nil, nil, nil, server.DeploymentModeManaged, nil, nil, nil, nil, server.Features{ServingAdmission: &middleware.ServingAdmissionConfig{}})
	for _, credential := range []string{"", "rk_public-key"} {
		request := httptest.NewRequest(http.MethodPost, policyregistry.WorkerValidationPath, nil)
		request.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		assert.Equal(t, http.StatusUnauthorized, response.Code)
	}
}

func TestManagedCatalogMetadataRemainsPublic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	server.RegisterWithFeatures(engine, nil, nil, fakeDeployedModelsSource{}, nil, server.DeploymentModeManaged, nil, nil, nil, nil, server.Features{ServingAdmission: &middleware.ServingAdmissionConfig{}})
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/router/models?scope=catalog", nil))
	assert.Equal(t, http.StatusOK, response.Code)
}
