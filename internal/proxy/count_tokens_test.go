package proxy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const countTokensBody = `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello world"}]}`

func countTokensService(provider *fakeProvider) *proxy.Service {
	return makeProxyService(router.Decision{}, map[string]providers.Client{providers.ProviderAnthropic: provider}).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
}

func countTokensRequest() *http.Request {
	return httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(""))
}

func assertLocalEstimate(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("content-type"))
	tokens := gjson.GetBytes(rec.Body.Bytes(), "input_tokens")
	require.True(t, tokens.Exists())
	assert.Positive(t, tokens.Int())
}

func TestCountTokens_UpstreamAnswerIsForwardedVerbatim(t *testing.T) {
	provider := &fakeProvider{passthroughResponse: func(_ context.Context, w http.ResponseWriter) error {
		w.Header().Set("content-type", "application/json")
		w.Header().Set("request-id", "req_123")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
		return nil
	}}
	rec := httptest.NewRecorder()

	require.NoError(t, countTokensService(provider).PassthroughToProvider(context.Background(), []byte(countTokensBody), rec, countTokensRequest()))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `{"input_tokens":42}`, rec.Body.String())
	assert.Equal(t, "req_123", rec.Header().Get("request-id"))
}

// Prod 2026-09-02: upstream count_tokens exceeded the route budget and the
// handler's context deadline surfaced as a hard 502, blocking the SDK's
// pre-flight and therefore the /v1/messages call behind it.
func TestCountTokens_UpstreamTimeoutFallsBackToLocalEstimate(t *testing.T) {
	provider := &fakeProvider{passthroughResponse: func(ctx context.Context, _ http.ResponseWriter) error {
		return errors.Join(errors.New("upstream passthrough call"), context.DeadlineExceeded)
	}}
	rec := httptest.NewRecorder()

	require.NoError(t, countTokensService(provider).PassthroughToProvider(context.Background(), []byte(countTokensBody), rec, countTokensRequest()))

	assertLocalEstimate(t, rec)
}

func TestCountTokens_UpstreamBudgetIsBoundedBelowRouteTimeout(t *testing.T) {
	var sawDeadline bool
	provider := &fakeProvider{passthroughResponse: func(ctx context.Context, _ http.ResponseWriter) error {
		_, sawDeadline = ctx.Deadline()
		return errors.New("dial tcp: connection refused")
	}}
	rec := httptest.NewRecorder()

	require.NoError(t, countTokensService(provider).PassthroughToProvider(context.Background(), []byte(countTokensBody), rec, countTokensRequest()))

	assert.True(t, sawDeadline, "upstream attempt must carry its own deadline so the fallback still fits in the route budget")
	assertLocalEstimate(t, rec)
}

func TestCountTokens_RetryableUpstreamStatusFallsBackToLocalEstimate(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, 529} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			provider := &fakeProvider{passthroughResponse: func(_ context.Context, w http.ResponseWriter) error {
				w.Header().Set("content-type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
				return &providers.UpstreamStatusError{Status: status}
			}}
			rec := httptest.NewRecorder()

			require.NoError(t, countTokensService(provider).PassthroughToProvider(context.Background(), []byte(countTokensBody), rec, countTokensRequest()))

			assertLocalEstimate(t, rec)
			assert.NotContains(t, rec.Body.String(), "overloaded_error", "upstream error body must not leak past the local answer")
		})
	}
}

func TestCountTokens_ClientFaultStatusIsReplayedVerbatim(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstreamErr := &providers.UpstreamStatusError{Status: status}
			provider := &fakeProvider{passthroughResponse: func(_ context.Context, w http.ResponseWriter) error {
				w.Header().Set("content-type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
				return upstreamErr
			}}
			rec := httptest.NewRecorder()

			err := countTokensService(provider).PassthroughToProvider(context.Background(), []byte(countTokensBody), rec, countTokensRequest())

			var got *providers.UpstreamStatusError
			require.ErrorAs(t, err, &got)
			assert.Equal(t, status, got.Status)
			assert.Equal(t, status, rec.Code)
			assert.Contains(t, rec.Body.String(), "invalid_request_error")
		})
	}
}

func TestCountTokens_DeploymentFaultIsNotMasked(t *testing.T) {
	provider := &fakeProvider{passthroughResponse: func(_ context.Context, _ http.ResponseWriter) error {
		return providers.ErrNotImplemented
	}}
	rec := httptest.NewRecorder()

	err := countTokensService(provider).PassthroughToProvider(context.Background(), []byte(countTokensBody), rec, countTokensRequest())

	require.ErrorIs(t, err, providers.ErrNotImplemented)
	assert.Empty(t, rec.Body.String())
}

func TestCountTokens_UnparseableBodyReplaysUpstreamFailure(t *testing.T) {
	transportErr := errors.New("dial tcp: connection refused")
	provider := &fakeProvider{passthroughResponse: func(_ context.Context, _ http.ResponseWriter) error {
		return transportErr
	}}
	rec := httptest.NewRecorder()

	err := countTokensService(provider).PassthroughToProvider(context.Background(), []byte(`not json`), rec, countTokensRequest())

	require.ErrorIs(t, err, transportErr)
	assert.Empty(t, rec.Body.String())
}
