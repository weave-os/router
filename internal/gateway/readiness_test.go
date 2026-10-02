package gateway_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/gateway"
	"weave-os/router/internal/policyregistry"
)

type readinessStore struct {
	bindingStore
	failure         error
	artifactFailure error
}

func (s readinessStore) ReadServingState(ctx context.Context, target policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	if s.failure != nil {
		return policyregistry.ServingStateSnapshot{}, s.failure
	}
	return s.bindingStore.ReadServingState(ctx, target)
}

func (s readinessStore) ReadServingObject(ctx context.Context, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	if s.artifactFailure != nil {
		return nil, nil, s.artifactFailure
	}
	return s.bindingStore.ReadServingObject(ctx, kind, ref)
}

type readinessAuthorizer struct{ failure error }

func (a readinessAuthorizer) IdentityToken(context.Context, string) (string, error) {
	return "readiness-iam", a.failure
}

func readinessFixture(t *testing.T, environment policyregistry.Environment, registryError, iamError error) *gateway.Handler {
	t.Helper()
	return readinessFixtureWithArtifacts(t, environment, registryError, nil, iamError)
}

func readinessFixtureWithArtifacts(t *testing.T, environment policyregistry.Environment, registryError, artifactError, iamError error) *gateway.Handler {
	t.Helper()
	binding := gatewayBinding("https://worker.example")
	if environment == policyregistry.EnvironmentStaging {
		binding.Target = policyregistry.TargetStaging
	}
	signer, err := policyregistry.NewAssertionSigner([]byte(strings.Repeat("s", 32)), time.Now)
	require.NoError(t, err)
	store := readinessStore{bindingStore: bindingStore{binding: binding}, failure: registryError, artifactFailure: artifactError}
	forwarder, err := gateway.NewHandler(credentialVerifier{}, &admissionStore{}, store, signer, readinessAuthorizer{iamError}, http.DefaultTransport, gateway.ProductSurfaces{Environment: environment, Analytics: &analyticsVerifier{}, Reads: &readVerifier{}})
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

func TestGatewayStartupRequiresActivatedBinding(t *testing.T) {
	for _, test := range []struct {
		name           string
		databaseError  error
		registryError  error
		artifactError  error
		expectedStatus int
	}{
		{name: "activation missing", registryError: policyregistry.ErrNotFound, expectedStatus: http.StatusServiceUnavailable},
		{name: "activated artifacts missing", artifactError: policyregistry.ErrNotFound, expectedStatus: http.StatusServiceUnavailable},
		{name: "registry unavailable", registryError: errors.New("private dependency diagnostic"), expectedStatus: http.StatusServiceUnavailable},
		{name: "database unavailable", databaseError: errors.New("private dependency diagnostic"), expectedStatus: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			forwarder := readinessFixtureWithArtifacts(t, policyregistry.EnvironmentStaging, test.registryError, test.artifactError, nil)
			err := forwarder.Warmup(context.Background(), func(context.Context) error { return test.databaseError })
			require.Error(t, err)
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

func TestGatewayWarmupUsesRetainedForwardingTransportWithoutAdmission(t *testing.T) {
	var calls atomic.Int32
	var firstConnection string
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/readyz" {
			firstConnection = r.RemoteAddr
			require.Equal(t, "Bearer gateway-iam", r.Header.Get(policyregistry.ServerlessAuthorizationHeader))
			require.Empty(t, r.Header.Get(policyregistry.ServingAssertionHeader))
			_, _ = io.WriteString(w, "ready")
			return
		}
		require.Equal(t, firstConnection, r.RemoteAddr)
		_, _ = io.WriteString(w, "served")
	}))
	defer worker.Close()
	forwarder, admissions, _ := gatewayFixture(t, worker, nil, nil, gateway.ProductSurfaces{Environment: policyregistry.EnvironmentProd, Analytics: &analyticsVerifier{}, Reads: &readVerifier{}})
	require.NoError(t, forwarder.Warmup(context.Background(), func(context.Context) error { return nil }))
	require.Equal(t, int32(1), calls.Load())
	require.Empty(t, admissions.seenConversation)
	// The same transport is used by the next real forwarding request.
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"synthetic startup test"}`))
	request.Header.Set("x-weave-api-key", "rk_credential")
	forwarder.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "served", response.Body.String())
	require.Equal(t, int32(2), calls.Load())
}

func TestGatewayWarmupRejectsWorkerErrorsAndRedirects(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusServiceUnavailable, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer worker.Close()
			signer, err := policyregistry.NewAssertionSigner([]byte(strings.Repeat("s", 32)), time.Now)
			require.NoError(t, err)
			forwarder, err := gateway.NewHandler(credentialVerifier{}, &admissionStore{}, bindingStore{binding: gatewayBinding(worker.URL)}, signer, readinessAuthorizer{}, worker.Client().Transport, gateway.ProductSurfaces{Environment: policyregistry.EnvironmentProd, Analytics: &analyticsVerifier{}, Reads: &readVerifier{}})
			require.NoError(t, err)
			require.ErrorContains(t, forwarder.Warmup(context.Background(), func(context.Context) error { return nil }), "worker startup returned status")
		})
	}
}
