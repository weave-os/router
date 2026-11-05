package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server/middleware"
)

func TestPinnedRuntimeCannotFallBackToInstallationOrHeaderStrategy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	snapshot := &policyregistry.Snapshot{}
	for _, header := range []router.Strategy{router.StrategyCluster, router.StrategyRL, router.StrategyHMM} {
		engine := gin.New()
		engine.Use(middleware.WithPinnedRuntime(&policyregistry.PinnedRuntime{Snapshot: snapshot}))
		engine.Use(func(c *gin.Context) {
			c.Set("router_installation", &auth.Installation{ID: "synthetic-local", RoutingStrategy: router.StrategyCluster, PolicyHeaderOverridesEnabled: true})
		})
		engine.Use(middleware.WithRouterStrategyDefault(router.StrategyCluster, allRegisteredLive, router.StrategyHMMEmbedding, router.StrategyRL))
		engine.GET("/probe", func(c *gin.Context) {
			require.Equal(t, router.StrategyHMMEmbedding, router.StrategyFromContext(c.Request.Context()))
			require.Same(t, snapshot, policyregistry.ServingSnapshotFromContext(c.Request.Context()))
			_, fabricated := requestcontext.ServingIdentityFromContext(c.Request.Context())
			require.False(t, fabricated, "a local snapshot is not managed admission")
			c.Status(http.StatusOK)
		})
		request := httptest.NewRequest(http.MethodGet, "/probe", nil)
		request.Header.Set(middleware.RouterStrategyOverrideHeader, string(header))
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
	}
}

func TestPinnedRuntimeMissingStrategyFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.WithPinnedRuntime(&policyregistry.PinnedRuntime{Snapshot: &policyregistry.Snapshot{}}))
	engine.Use(func(c *gin.Context) { c.Set("router_installation", &auth.Installation{ID: "synthetic-local"}) })
	engine.Use(middleware.WithRouterStrategyDefault(router.StrategyCluster, allRegisteredLive, router.StrategyRL))
	engine.GET("/probe", func(c *gin.Context) { t.Fatal("missing pinned router reached request handler") })
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/probe", nil))
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
}
