package middleware

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
)

func TestRandomAvailabilityIsWorkerOwnedAndHasNoPrimaryFallback(t *testing.T) {
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequestWithContext(context.Background(), "POST", "/v1/responses", nil)
	c.Request = c.Request.WithContext(policyregistry.WithServingSnapshot(context.Background(), &policyregistry.Snapshot{Routers: map[router.Strategy]router.Router{}}))
	require.True(t, routingStrategyServable(c, true, router.StrategyRandomBenchmark, func(strategy router.Strategy) bool { return strategy == router.StrategyRandomBenchmark }))
	require.False(t, routingStrategyServable(c, true, router.StrategyRandomBenchmark, func(router.Strategy) bool { return false }))
	policy, ok := router.RoutingModelPolicyFor(router.ModelRandom)
	require.True(t, ok)
	require.False(t, policy.FallsBackToPrimary)
}
