package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"weave-os/router/internal/router"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/rosterdata"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"
)

type unavailableDistributionRoster struct{}

func (unavailableDistributionRoster) DistributionRoster(context.Context, router.Strategy) (*rosterdata.Roster, error) {
	return nil, errors.New("no active policy")
}

func TestSelfHostedDistributionComesFromConfiguredRosterSource(t *testing.T) {
	gin.SetMode(gin.TestMode)

	unwired := gin.New()
	server.Register(unwired, nil, nil, fakeHMMRosterSource{}, server.DeploymentModeSelfHosted, nil, nil, nil, nil)
	assert.NotContains(t, routeSet(unwired), "GET /v1/router/routing-distribution",
		"without an HMM distribution source no legacy projection may be mounted")

	engine := gin.New()
	server.RegisterWithFeatures(engine, nil, nil, fakeHMMRosterSource{}, server.DeploymentModeSelfHosted, nil, nil, nil, nil, server.Features{RoutingDistribution: selection.DistributionSource{Rosters: unavailableDistributionRoster{}}})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/routing-distribution?grid=2", nil))
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code, "the configured roster source must back the route")
}

func TestServingDistributionMountsWithoutLegacyScorerAndRequiresAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	server.RegisterWithFeatures(engine, nil, nil, nil, server.DeploymentModeManaged, nil, nil, nil, nil, server.Features{ServingAdmission: &middleware.ServingAdmissionConfig{}, RoutingDistribution: selection.DistributionSource{Rosters: policyregistry.AdmittedRosterSource{}}})
	assert.Contains(t, routeSet(engine), "GET /v1/router/routing-distribution")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/routing-distribution?grid=2", nil))
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}
