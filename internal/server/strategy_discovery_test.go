package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/server"
)

type fixtureClusterRoster struct{}

func (fixtureClusterRoster) ClusterRoster(context.Context, router.Strategy) (policy.RosterSnapshot, error) {
	return policy.RosterSnapshot{Clusters: map[string][]string{"high": {"provider/model-a:high"}}}, nil
}

func registerDiscovery(t *testing.T, features server.Features) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	sources := map[router.Strategy]policy.RosterSource{router.StrategyHMM: fixtureClusterRoster{}}
	server.RegisterWithFeatures(engine, nil, nil, nil, server.DeploymentModeSelfHosted, nil, nil, sources, nil, features)
	return engine
}

func TestHMMRosterRouteRequiresCatalogProjection(t *testing.T) {
	assert.NotContains(t, routeSet(registerDiscovery(t, server.Features{})), "GET /v1/router/hmm-roster",
		"without a catalog projection the roster route stays unmounted")

	engine := registerDiscovery(t, server.Features{RosterCatalogID: func(arm string) string { return "catalog:" + arm }})
	require.Contains(t, routeSet(engine), "GET /v1/router/hmm-roster")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/hmm-roster", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	var body struct {
		Clusters []struct {
			Models []string `json:"models"`
		} `json:"clusters"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Len(t, body.Clusters, 1)
	assert.Equal(t, []string{"catalog:provider/model-a:high"}, body.Clusters[0].Models, "the wired catalog projection backs the route")
}

func TestHMMRosterRouteRequiresRosterSources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	server.RegisterWithFeatures(engine, nil, nil, nil, server.DeploymentModeSelfHosted, nil, nil, nil, nil, server.Features{RosterCatalogID: func(arm string) string { return arm }})
	assert.NotContains(t, routeSet(engine), "GET /v1/router/hmm-roster")
}

func TestTaskDomainPreviewRouteRequiresPreviewer(t *testing.T) {
	t.Setenv("ROUTER_INTERNAL_SERVICE_TOKEN", "internal-fixture-token")
	const route = "POST /internal/v1/task-domain/preview"
	assert.NotContains(t, routeSet(registerDiscovery(t, server.Features{})), route,
		"without a previewer the preview route stays unmounted")
	assert.Contains(t, routeSet(registerDiscovery(t, server.Features{TaskDomainPreview: selection.TaskDomainPreviewer{}})), route)
}
