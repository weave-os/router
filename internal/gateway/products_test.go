package gateway_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/gateway"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/translate"
)

type feedbackLookup struct {
	admission             policyregistry.SessionReleaseBinding
	installation, request string
	err                   error
	calls                 int
}

func (f *feedbackLookup) GetFeedbackAdmission(_ context.Context, installation, request string) (policyregistry.SessionReleaseBinding, error) {
	f.installation, f.request = installation, request
	f.calls++
	return f.admission, f.err
}

type analyticsVerifier struct {
	token string
	err   error
}

func (v *analyticsVerifier) VerifyAnalyticsCredential(_ context.Context, token string) error {
	v.token = token
	return v.err
}

func TestFeedbackUsesOriginalBindingWithoutAdmissionOrInferenceAssertion(t *testing.T) {
	var calls atomic.Int32
	var observedBody string
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Empty(t, r.Header.Get(policyregistry.ServingAssertionHeader))
		assert.Empty(t, r.Header.Get("X-Weave-Serving-Target"))
		assert.Equal(t, "Bearer gateway-iam", r.Header.Get(policyregistry.ServerlessAuthorizationHeader))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		observedBody = string(body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer worker.Close()
	signer := feedback.NewSigner("feedback-secret", time.Hour)
	lookup := &feedbackLookup{}
	products := gateway.ProductSurfaces{Environment: policyregistry.EnvironmentProd, Analytics: &analyticsVerifier{}, Feedback: signer, Attribution: lookup}
	forwarder, admissions, _ := gatewayFixture(t, worker, auth.ErrInvalidToken, errors.New("must not re-admit feedback"), products)
	lookup.admission = admissions.admission
	// The original activation may no longer be current; feedback must not read a head.
	lookup.admission.ActivationID = "retired-activation"
	token := signer.Mint("original-installation", "org", "original-request", "user")
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/feedback/link/" + token, ""},
		{http.MethodGet, "/v1/feedback/rate?t=" + token + "&r=" + translate.RouterFeedbackRatingUp, ""},
		{http.MethodPost, "/v1/feedback/link", "{ \"token\":\"" + token + "\", \"rating\":\"" + translate.RouterFeedbackRatingDown + "\" }"},
	} {
		r := httptest.NewRequest(request.method, request.path, strings.NewReader(request.body))
		r.Header.Set(policyregistry.ServingAssertionHeader, "spoofed")
		r.Header.Set("X-Weave-Serving-Target", string(policyregistry.TargetInternal))
		w := httptest.NewRecorder()
		forwarder.ServeHTTP(w, r)
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		assert.Equal(t, request.body, observedBody)
		assert.Equal(t, "original-installation", lookup.installation)
		assert.Equal(t, "original-request", lookup.request)
	}
	assert.Equal(t, int32(3), calls.Load())
	assert.Empty(t, admissions.seenConversation)
}

func TestFeedbackRejectsInvalidExpiredMissingOrForeignAttribution(t *testing.T) {
	var calls atomic.Int32
	worker := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer worker.Close()
	signer := feedback.NewSigner("feedback-secret", time.Hour)
	expired := feedback.NewSigner("feedback-secret", time.Nanosecond).Mint("installation", "org", "request", "")
	for _, test := range []struct {
		name, token     string
		err             error
		target          policyregistry.ServingTarget
		status, lookups int
	}{
		{"invalid", "bad.token", nil, policyregistry.TargetStable, http.StatusNotFound, 0},
		{"expired", expired, nil, policyregistry.TargetStable, http.StatusGone, 0},
		{"no attribution", signer.Mint("installation", "org", "request", ""), sql.ErrNoRows, policyregistry.TargetStable, http.StatusNotFound, 1},
		{"storage outage", signer.Mint("installation", "org", "request", ""), context.DeadlineExceeded, policyregistry.TargetStable, http.StatusServiceUnavailable, 1},
		{"foreign environment", signer.Mint("installation", "org", "request", ""), nil, policyregistry.TargetStaging, http.StatusServiceUnavailable, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookup := &feedbackLookup{err: test.err}
			forwarder, admissions, _ := gatewayFixture(t, worker, nil, nil, gateway.ProductSurfaces{Environment: policyregistry.EnvironmentProd, Analytics: &analyticsVerifier{}, Feedback: signer, Attribution: lookup})
			lookup.admission = admissions.admission
			lookup.admission.Target = test.target
			w := httptest.NewRecorder()
			forwarder.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/feedback/link/"+test.token, nil))
			assert.Equal(t, test.status, w.Code)
			assert.Equal(t, test.lookups, lookup.calls)
		})
	}
	assert.Zero(t, calls.Load())
}

func TestFeedbackBodyLimitAndDisabledFeature(t *testing.T) {
	worker := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected dispatch") }))
	defer worker.Close()
	lookup := &feedbackLookup{}
	products := gateway.ProductSurfaces{Environment: policyregistry.EnvironmentProd, Analytics: &analyticsVerifier{}, Feedback: feedback.NewSigner("feedback-secret", time.Hour), Attribution: lookup}
	forwarder, _, _ := gatewayFixture(t, worker, nil, nil, products)
	for _, body := range []string{strings.Repeat("x", 64*1024+1), "{", `{"token":123}`} {
		w := httptest.NewRecorder()
		forwarder.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/feedback/link", strings.NewReader(body)))
		assert.Equal(t, http.StatusBadRequest, w.Code)
	}
	assert.Zero(t, lookup.calls)
	products.Feedback = nil
	forwarder, _, _ = gatewayFixture(t, worker, nil, nil, products)
	w := httptest.NewRecorder()
	forwarder.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/feedback/link/bad.token", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAnalyticsNeverUsesRoutingAdmissionOrAssertion(t *testing.T) {
	var calls atomic.Int32
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Empty(t, r.Header.Get(policyregistry.ServingAssertionHeader))
		assert.Equal(t, "Bearer ra_analytics", r.Header.Get("Authorization"))
		assert.Equal(t, "Bearer gateway-iam", r.Header.Get(policyregistry.ServerlessAuthorizationHeader))
		w.WriteHeader(http.StatusOK)
	}))
	defer worker.Close()
	analytics := &analyticsVerifier{}
	forwarder, _, _ := gatewayFixture(t, worker, auth.ErrInvalidToken, errors.New("must not admit analytics"), gateway.ProductSurfaces{Environment: policyregistry.EnvironmentProd, Analytics: analytics})
	for _, path := range []string{"/v1/analytics/routing-decisions?limit=2", "/v1/analytics/models", "/v1/analytics/schema"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer ra_analytics")
		r.Header.Set(policyregistry.ServingAssertionHeader, "spoofed")
		forwarder.ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "ra_analytics", analytics.token)
	}
	analytics.err = auth.ErrWrongKeyScope
	w := httptest.NewRecorder()
	forwarder.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/analytics/schema", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, int32(3), calls.Load())
}

func TestPublicVersionAndFeedbackAssetsRemainKeyless(t *testing.T) {
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get(policyregistry.ServingAssertionHeader))
		w.WriteHeader(http.StatusOK)
	}))
	defer worker.Close()
	forwarder, _, _ := gatewayFixture(t, worker, auth.ErrInvalidToken, errors.New("must not admit assets"), gateway.ProductSurfaces{Environment: policyregistry.EnvironmentProd, Analytics: &analyticsVerifier{err: auth.ErrInvalidToken}, Feedback: feedback.NewSigner("secret", 0), Attribution: &feedbackLookup{}})
	for _, path := range []string{"/v1/version", "/v1/feedback/assets/wooly-wave.png", "/v1/feedback/assets/weave.svg"} {
		w := httptest.NewRecorder()
		forwarder.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
}

func TestSubscriptionsPreserveMethodsAndBindAssertions(t *testing.T) {
	type requestBytes struct {
		request *http.Request
		body    []byte
	}
	observed := make(chan requestBytes, 1)
	worker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		observed <- requestBytes{r.Clone(context.Background()), body}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer worker.Close()
	forwarder, _, signer := gatewayFixture(t, worker, nil, nil)
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/subscriptions/accounts", ""},
		{http.MethodPost, "/v1/subscriptions/accounts", `{"provider":"` + string(auth.SubscriptionProviderCodex) + `","refresh_token":"secret"}`},
		{http.MethodPatch, "/v1/subscriptions/accounts/account", `{"enabled":false}`},
		{http.MethodDelete, "/v1/subscriptions/accounts/account", ""},
	} {
		r := httptest.NewRequest(request.method, request.path, strings.NewReader(request.body))
		r.Header.Set(auth.RouterKeyHeader, "rk_credential")
		w := httptest.NewRecorder()
		forwarder.ServeHTTP(w, r)
		require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
		seen := <-observed
		assert.Equal(t, request.method, seen.request.Method)
		assert.Equal(t, request.path, seen.request.URL.Path)
		assert.Equal(t, request.body, string(seen.body))
		_, err := signer.Verify(seen.request.Header.Get(policyregistry.ServingAssertionHeader), r, seen.body, "rk_credential")
		require.NoError(t, err)
	}
}

func TestGatewayProductWhitelistRejectsNestedAndUnknownPaths(t *testing.T) {
	worker := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected dispatch") }))
	defer worker.Close()
	forwarder, _, _ := gatewayFixture(t, worker, nil, nil, gateway.ProductSurfaces{Environment: policyregistry.EnvironmentProd, Analytics: &analyticsVerifier{}})
	for _, path := range []string{"/v1/models/", "/v1/models/a/b", "/v1/sessions/a/b/cost", "/v1/sessions//cost", "/v1/subscriptions/accounts/account/extra", "/v1/analytics/unknown", "/v1/feedback/link/token/nested", "/v1/feedback/assets/unknown", "/admin/v1/config"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
			w := httptest.NewRecorder()
			forwarder.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(`{}`)))
			assert.Equal(t, http.StatusNotFound, w.Code, method+" "+path)
		}
	}
}
