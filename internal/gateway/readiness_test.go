package gateway_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/gateway"
	"weave-os/router/internal/policyregistry"
)

type readinessStore struct {
	bindingStore
	failure error
}

func (s readinessStore) ReadServingState(ctx context.Context, target policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	if s.failure != nil {
		return policyregistry.ServingStateSnapshot{}, s.failure
	}
	return s.bindingStore.ReadServingState(ctx, target)
}

type readinessAuthorizer struct{ failure error }

func (a readinessAuthorizer) IdentityToken(context.Context, string) (string, error) {
	return "readiness-iam", a.failure
}

func readinessFixture(t *testing.T, environment policyregistry.Environment, registryError, iamError error) *gateway.Handler {
	t.Helper()
	binding := gatewayBinding("https://worker.example")
	if environment == policyregistry.EnvironmentStaging {
		binding.Target = policyregistry.TargetStaging
	}
	signer, err := policyregistry.NewAssertionSigner([]byte(strings.Repeat("s", 32)), time.Now)
	require.NoError(t, err)
	forwarder, err := gateway.NewHandler(credentialVerifier{}, &admissionStore{}, readinessStore{bindingStore{binding: binding}, registryError}, signer, readinessAuthorizer{iamError}, http.DefaultTransport, gateway.ProductSurfaces{Environment: environment, Analytics: &analyticsVerifier{}})
	require.NoError(t, err)
	return forwarder
}

func TestGatewayReadinessChecksAdmissionDependencies(t *testing.T) {
	unavailable := errors.New("private dependency diagnostic")
	for _, test := range []struct {
		name           string
		environment    policyregistry.Environment
		databaseError  error
		registryError  error
		iamError       error
		expectedStatus int
	}{
		{name: "production ready", environment: policyregistry.EnvironmentProd, expectedStatus: http.StatusOK},
		{name: "staging ready", environment: policyregistry.EnvironmentStaging, expectedStatus: http.StatusOK},
		{name: "database unavailable", environment: policyregistry.EnvironmentProd, databaseError: unavailable, expectedStatus: http.StatusServiceUnavailable},
		{name: "registry unavailable", environment: policyregistry.EnvironmentProd, registryError: unavailable, expectedStatus: http.StatusServiceUnavailable},
		{name: "activation missing", environment: policyregistry.EnvironmentProd, registryError: policyregistry.ErrNotFound, expectedStatus: http.StatusServiceUnavailable},
		{name: "IAM unavailable", environment: policyregistry.EnvironmentProd, iamError: unavailable, expectedStatus: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			forwarder := readinessFixture(t, test.environment, test.registryError, test.iamError)
			probe := forwarder.ReadinessHandler(func(context.Context) error { return test.databaseError })
			response := httptest.NewRecorder()
			probe.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			require.Equal(t, test.expectedStatus, response.Code)
			require.NotContains(t, response.Body.String(), unavailable.Error())
		})
	}
}

func TestGatewayReadinessDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		forwarder := readinessFixture(t, policyregistry.EnvironmentProd, nil, nil)
		probe := forwarder.ReadinessHandler(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		response := httptest.NewRecorder()
		started := time.Now()
		probe.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		require.Equal(t, http.StatusServiceUnavailable, response.Code)
		require.Equal(t, 5*time.Second, time.Since(started))
	})
}
