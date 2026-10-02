package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/health"
)

func TestGatewayCapacityProbeBypassesForwardingAndPreservesLiveness(t *testing.T) {
	capacity, err := health.NewCapacity(health.Limits{MaxRequests: 1})
	require.NoError(t, err)
	permit := capacity.TryAcquire()
	require.NotNil(t, permit)
	defer permit.Release()
	unexpected := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("capacity and liveness must not call a dependency") })
	entry := gatewayHTTPHandler(unexpected, unexpected, unexpected, capacity.Handler())
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, test := range []struct {
			path   string
			status int
		}{
			{"/health", http.StatusOK},
			{"/capacityz", http.StatusServiceUnavailable},
		} {
			response := httptest.NewRecorder()
			entry.ServeHTTP(response, httptest.NewRequest(method, test.path, nil))
			assert.Equal(t, test.status, response.Code)
		}
	}
}
