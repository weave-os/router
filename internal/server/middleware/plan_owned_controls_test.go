package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

type planOwnedAllowedModelsGate struct{}

func (planOwnedAllowedModelsGate) ResolveAllowedModelsHeader(context.Context) bool { return true }

func TestMaxServingForcesProfileStrategyAndIgnoresCustomerHeaders(t *testing.T) {
	t.Parallel()

	plan := entitlement.PlanMax
	t.Run(string(plan), func(t *testing.T) {
		engine := gin.New()
		engine.Use(func(c *gin.Context) {
			c.Set("router_installation", &auth.Installation{
				ID:                           "installation",
				PolicyHeaderOverridesEnabled: true,
				RoutingStrategy:              router.StrategyRL,
			})
			ctx := requestcontext.WithServingIdentity(c.Request.Context(), requestcontext.ServingIdentity{Plan: string(plan)})
			c.Request = c.Request.WithContext(ctx)
			c.Next()
		})
		engine.Use(
			middleware.WithClusterVersionOverride(),
			middleware.WithRouterStrategyDefault(router.StrategyCluster, allRegisteredLive, router.StrategyHMM, router.StrategyRL),
			middleware.WithAllowedModelsOverride(planOwnedAllowedModelsGate{}),
			middleware.WithRoutingKnobsOverride(),
			middleware.WithForceEffortOverride(),
			middleware.WithPolicyPinOverride(),
		)
		var strategy router.Strategy
		engine.GET("/probe", func(c *gin.Context) {
			strategy = router.StrategyFromContext(c.Request.Context())
			c.Status(http.StatusOK)
		})

		request := httptest.NewRequest(http.MethodGet, "/probe", nil)
		request.Header.Set(middleware.RouterStrategyOverrideHeader, "bandit")
		request.Header.Set(middleware.ClusterVersionOverrideHeader, "customer-roster")
		request.Header.Set(proxy.AllowedModelsHeader, "unknown-model")
		request.Header.Set(middleware.HeaderAlpha, "not-a-number")
		request.Header.Set(middleware.ForceEffortOverrideHeader, "invalid")
		request.Header.Set(middleware.PolicyPinOverrideHeader, "invalid")
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)

		assert.Equal(t, http.StatusOK, response.Code)
		assert.Equal(t, router.StrategyHMM, strategy)
	})
}

func TestBoostServingUsesInstallationStrategyAndAuthorizedOverride(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		header string
		want   router.Strategy
	}{
		{name: "installation default", want: router.StrategyRL},
		{name: "authorized override", header: string(router.StrategyHMM), want: router.StrategyHMM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				c.Set("router_installation", &auth.Installation{
					ID: "installation", PolicyHeaderOverridesEnabled: true,
					RoutingStrategy: router.StrategyRL,
				})
				ctx := requestcontext.WithServingIdentity(c.Request.Context(), requestcontext.ServingIdentity{Plan: string(entitlement.PlanBoost)})
				c.Request = c.Request.WithContext(ctx)
				c.Next()
			})
			engine.Use(middleware.WithRouterStrategyDefault(router.StrategyCluster, allRegisteredLive, router.StrategyHMM, router.StrategyRL))
			var strategy router.Strategy
			engine.GET("/probe", func(c *gin.Context) {
				strategy = router.StrategyFromContext(c.Request.Context())
				c.Status(http.StatusOK)
			})

			request := httptest.NewRequest(http.MethodGet, "/probe", nil)
			if tc.header != "" {
				request.Header.Set(middleware.RouterStrategyOverrideHeader, tc.header)
			}
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)

			assert.Equal(t, http.StatusOK, response.Code)
			assert.Equal(t, tc.want, strategy)
		})
	}
}

func TestPlanOwnedServingRejectsUnknownPlan(t *testing.T) {
	t.Parallel()

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: "installation"})
		ctx := requestcontext.WithServingIdentity(c.Request.Context(), requestcontext.ServingIdentity{Plan: "unknown"})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	engine.Use(middleware.WithRouterStrategyDefault(router.StrategyCluster, allRegisteredLive, router.StrategyHMM))
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/probe", nil))

	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
}
