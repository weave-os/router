package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/server"
)

// Without a proxy, the policy catalog still answers with an empty strategy
// list, whether the catalog port is absent or a typed-nil proxy catalog.
func TestPolicyCatalogWithoutProxyListsNoStrategies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, services := range map[string]server.Services{
		"no catalog port":       {},
		"typed-nil proxy ports": proxyServices(nil),
	} {
		t.Run(name, func(t *testing.T) {
			engine := gin.New()
			server.RegisterWithFeatures(engine, nil, services, nil, server.DeploymentModeManaged, nil, nil, nil, nil, server.Features{})
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/policies", nil))

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.JSONEq(t, `{"default_strategy":"","schema_version":"policy_router_v1","strategies":[]}`, recorder.Body.String())
		})
	}
}
