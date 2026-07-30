package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workweave/router/internal/auth"
	"workweave/router/internal/providers"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router"
	"workweave/router/internal/router/cluster"
	"workweave/router/internal/router/policy"
	"workweave/router/internal/server"

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
		"GET /health",
		"GET /readyz",
		"GET /validate",
		"GET /v1/router/models",
		"POST /v1/messages",
		"POST /v1/chat/completions",
		"POST /v1/responses",
		"POST /v1/route",
		"POST /v1/route/preview",
		"POST /v1/messages/count_tokens",
		"GET /v1/models",
		"GET /v1/models/:model",
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
		"DELETE /admin/v1/provider-keys/:id",
		"GET /admin/v1/config",
		"GET /admin/v1/excluded-models",
		"PUT /admin/v1/excluded-models",
	}

	t.Run("selfhosted mounts dashboard and product routes", func(t *testing.T) {
		engine := gin.New()
		// Nil services are fine: engine.Routes() inspection never invokes the closure-captured handlers.
		server.Register(engine, nil, nil, fakeDeployedModelsSource{}, nil, server.DeploymentModeSelfHosted, nil, nil, nil)
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
		server.Register(engine, nil, nil, fakeDeployedModelsSource{}, nil, server.DeploymentModeManaged, nil, nil, nil)
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
		server.Register(engine, nil, nil, nil, nil, server.DeploymentModeManaged, nil, nil, nil)
		got := routeSet(engine)
		assert.NotContains(t, got, "GET /v1/router/models", "catalog endpoint must not mount without a deployed-models source")
	})
}

// --- preview / route request-shaping parity ---

// alwaysAuthAPIKeyRepository authenticates any rk_-prefixed token as one fixed
// installation, so route-registration tests can exercise middleware that runs
// after WithAuth without a DB.
type alwaysAuthAPIKeyRepository struct {
	installation *auth.Installation
}

func (r *alwaysAuthAPIKeyRepository) Create(context.Context, auth.CreateAPIKeyParams) (*auth.APIKey, error) {
	return nil, errors.New("not used")
}

func (r *alwaysAuthAPIKeyRepository) GetActiveByHashWithInstallation(context.Context, string) (*auth.APIKey, *auth.Installation, error) {
	return &auth.APIKey{ID: "key-1", InstallationID: r.installation.ID}, r.installation, nil
}

func (r *alwaysAuthAPIKeyRepository) ListForInstallation(context.Context, string) ([]*auth.APIKey, error) {
	return nil, errors.New("not used")
}

func (r *alwaysAuthAPIKeyRepository) MarkUsed(context.Context, string) error { return nil }

func (r *alwaysAuthAPIKeyRepository) SoftDelete(context.Context, string, string) error {
	return errors.New("not used")
}

// capturingPreviewRouter records the router.Request that reached it so a test
// can assert which request-shaping middleware ran.
type capturingPreviewRouter struct {
	got *router.Request
}

func (c *capturingPreviewRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	c.got = &req
	return router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5"}, nil
}

func (c *capturingPreviewRouter) PreviewRoute(_ context.Context, req router.Request) (policy.PreviewResult, error) {
	c.got = &req
	return policy.PreviewResult{SchemaVersion: policy.SchemaVersionV1}, nil
}

// TestRegisterPreviewHonorsForceEffort guards request-shaping parity between
// /v1/route and /v1/route/preview. ForceEffort feeds policy arm hashing, so a
// preview group missing WithForceEffortOverride would silently return a
// decision trace for a different arm than /v1/route serves for the same
// headers — and the documented contract plus both SDKs promise they agree.
func TestRegisterPreviewHonorsForceEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, path := range []string{"/v1/route", "/v1/route/preview"} {
		t.Run(path, func(t *testing.T) {
			capturing := &capturingPreviewRouter{}
			proxySvc := proxy.NewService(capturing, map[string]providers.Client{}, nil, false, nil, nil, false, "", "", nil).
				WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: capturing})
			authSvc := auth.NewService(
				nil,
				&alwaysAuthAPIKeyRepository{installation: &auth.Installation{ID: "inst-1", PolicyHeaderOverridesEnabled: true}},
				nil, nil, auth.NoOpAPIKeyCache{}, nil, nil,
			)

			engine := gin.New()
			server.Register(engine, authSvc, proxySvc, nil, nil, server.DeploymentModeManaged, nil, nil, nil)

			body := `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer rk_test_token")
			request.Header.Set("x-weave-router-strategy", string(router.StrategyHMM))
			request.Header.Set("x-weave-effort", "high")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)

			require.Equal(t, http.StatusOK, response.Code, "body: %s", response.Body.String())
			require.NotNil(t, capturing.got, "the router must have been reached")
			require.NotNil(t, capturing.got.RoutingKnobs, "x-weave-effort must reach the router as routing knobs")
			assert.Equal(t, "high", capturing.got.RoutingKnobs.ForceEffort)
		})
	}
}

// TestRegisterPreviewRejectsInvalidForceEffort pins the other half of the
// parity: an invalid effort value must be rejected on preview too, not silently
// dropped because the validating middleware never ran.
func TestRegisterPreviewRejectsInvalidForceEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, path := range []string{"/v1/route", "/v1/route/preview"} {
		t.Run(path, func(t *testing.T) {
			capturing := &capturingPreviewRouter{}
			proxySvc := proxy.NewService(capturing, map[string]providers.Client{}, nil, false, nil, nil, false, "", "", nil).
				WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: capturing})
			authSvc := auth.NewService(
				nil,
				&alwaysAuthAPIKeyRepository{installation: &auth.Installation{ID: "inst-1", PolicyHeaderOverridesEnabled: true}},
				nil, nil, auth.NoOpAPIKeyCache{}, nil, nil,
			)

			engine := gin.New()
			server.Register(engine, authSvc, proxySvc, nil, nil, server.DeploymentModeManaged, nil, nil, nil)

			body := `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer rk_test_token")
			request.Header.Set("x-weave-router-strategy", string(router.StrategyHMM))
			request.Header.Set("x-weave-effort", "not-an-effort-level")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)

			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Nil(t, capturing.got, "an invalid effort must be rejected before routing")
		})
	}
}

func TestRegisterSeparatesLivenessFromReadiness(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	checker := healthCheckerFunc(func(context.Context) error {
		return errors.New("dependency unavailable")
	})
	server.Register(engine, nil, nil, nil, nil, server.DeploymentModeManaged, nil, checker, nil)

	for _, test := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/health", wantStatus: http.StatusOK},
		{path: "/readyz", wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			assert.Equal(t, test.wantStatus, response.Code)
		})
	}
}
