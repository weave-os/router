package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"
)

func TestServingDistributionMountsWithoutLegacyScorerAndRequiresAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	server.RegisterWithFeatures(engine, nil, nil, nil, nil, server.DeploymentModeManaged, nil, nil, nil, nil, server.Features{ServingAdmission: &middleware.ServingAdmissionConfig{}})
	assert.Contains(t, routeSet(engine), "GET /v1/router/routing-distribution")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/router/routing-distribution?grid=2", nil))
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}
