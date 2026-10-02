package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/gateway"
	"weave-os/router/internal/policyregistry"
)

type discoveryStore struct {
	policyregistry.ServingStore
	bindings map[policyregistry.ServingTarget]bindingStore
}

func (s discoveryStore) RootURI() string { return registryRoot }

func (s discoveryStore) ReadServingState(ctx context.Context, target policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	return s.bindings[target].ReadServingState(ctx, target)
}

func (s discoveryStore) ReadServingObject(ctx context.Context, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	for _, binding := range s.bindings {
		manifest, payload, err := binding.ReadServingObject(ctx, kind, ref)
		if err == nil && policyregistry.Digest(payload) == ref.SHA256 {
			return manifest, payload, nil
		}
	}
	return nil, nil, policyregistry.ErrNotFound
}

func TestGatewayRoutingDiscoveryAuthenticatesAndForwardsToCurrentTarget(t *testing.T) {
	type observed struct {
		body  string
		token string
		path  string
	}
	stableRequests := make(chan observed, 2)
	internalRequests := make(chan observed, 2)
	worker := func(target policyregistry.ServingTarget, requests chan<- observed) *httptest.Server {
		return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			requests <- observed{body: string(body), token: r.Header.Get("X-Weave-Internal-Token"), path: r.URL.Path}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"target": string(target)})
		}))
	}
	stableWorker := worker(policyregistry.TargetStable, stableRequests)
	defer stableWorker.Close()
	internalWorker := worker(policyregistry.TargetInternal, internalRequests)
	defer internalWorker.Close()
	stableBinding := gatewayBinding(stableWorker.URL)
	internalBinding := gatewayBinding(internalWorker.URL)
	internalBinding.Target = policyregistry.TargetInternal
	store := discoveryStore{bindings: map[policyregistry.ServingTarget]bindingStore{
		policyregistry.TargetStable:   {binding: stableBinding},
		policyregistry.TargetInternal: {binding: internalBinding},
	}}
	signer, err := policyregistry.NewAssertionSigner([]byte(strings.Repeat("s", 32)), time.Now)
	require.NoError(t, err)
	admissions := &admissionStore{}
	forwarder, err := gateway.NewHandler(credentialVerifier{}, admissions, store, signer, revisionAuthorizer{}, stableWorker.Client().Transport,
		gateway.ProductSurfaces{Environment: policyregistry.EnvironmentProd, InternalToken: "internal-secret", Analytics: &analyticsVerifier{}, Reads: &readVerifier{}})
	require.NoError(t, err)

	for _, target := range []policyregistry.ServingTarget{policyregistry.TargetStable, policyregistry.TargetInternal} {
		body := `{"target":"` + string(target) + `","grid":2,"excluded_models":["gpt-5.6-luna"],"excluded_providers":["openai"]}`
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/routing-discovery", strings.NewReader(body))
		request.Header.Set("X-Weave-Internal-Token", "internal-secret")
		recorder := httptest.NewRecorder()
		forwarder.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), string(target))
		assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
		var reached observed
		if target == policyregistry.TargetStable {
			reached = <-stableRequests
		} else {
			reached = <-internalRequests
		}
		assert.Equal(t, body, reached.body)
		assert.Equal(t, "internal-secret", reached.token)
		assert.Equal(t, "/internal/v1/routing-discovery", reached.path)
	}
	assert.Empty(t, stableRequests)
	assert.Empty(t, internalRequests)
	assert.Empty(t, admissions.seenConversation)

	for _, testCase := range []struct {
		name   string
		body   string
		token  string
		status int
	}{
		{name: "no token", body: `{"target":"prod/stable"}`, status: http.StatusUnauthorized},
		{name: "wrong token", body: `{"target":"prod/stable"}`, token: "wrong", status: http.StatusUnauthorized},
		{name: "wrong environment", body: `{"target":"staging"}`, token: "internal-secret", status: http.StatusBadRequest},
		{name: "invalid profile", body: `{"target":"prod/stable","profile_key":"not-a-uuid"}`, token: "internal-secret", status: http.StatusBadRequest},
		{name: "oversized body", body: `{"target":"prod/stable","padding":"` + strings.Repeat("x", 17000) + `"}`, token: "internal-secret", status: http.StatusBadRequest},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/internal/v1/routing-discovery", strings.NewReader(testCase.body))
			request.Header.Set("X-Weave-Internal-Token", testCase.token)
			recorder := httptest.NewRecorder()
			forwarder.ServeHTTP(recorder, request)
			assert.Equal(t, testCase.status, recorder.Code)
		})
	}
	assert.Empty(t, stableRequests)
	assert.Empty(t, internalRequests)
}
