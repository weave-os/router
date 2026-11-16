package providers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
)

func TestCopyUpstreamHeadersDropsStaticAndConnectionNamedHopHeaders(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Header.Set("Transfer-Encoding", "chunked")
	resp.Header.Set("Keep-Alive", "timeout=5")
	resp.Header.Add("Connection", "x-session-hop, X-Other-Hop")
	resp.Header.Set("X-Session-Hop", "1")
	resp.Header.Set("X-Other-Hop", "2")
	resp.Header.Set("X-Request-Id", "req-1")

	recorder := httptest.NewRecorder()
	providers.CopyUpstreamHeaders(recorder, resp)

	got := recorder.Header()
	assert.Equal(t, "text/event-stream", got.Get("Content-Type"))
	assert.Equal(t, "req-1", got.Get("X-Request-Id"))
	for _, hop := range []string{"Connection", "Transfer-Encoding", "Keep-Alive", "X-Session-Hop", "X-Other-Hop"} {
		assert.Empty(t, got.Values(hop), hop)
	}
}

func TestGatewaysAreExclusiveAndNeedTheirOwnEndpoint(t *testing.T) {
	for _, provider := range providers.AllProviders() {
		gateway := provider == providers.ProviderAnthropicGateway || provider == providers.ProviderOpenAIGateway
		assert.Equal(t, gateway, providers.IsGateway(provider), provider)
		assert.Equal(t, gateway, providers.RequiresBaseURL(provider), provider)
	}
}

func TestOnlyXAIReportsReasoningOutsideOutputTokens(t *testing.T) {
	for _, provider := range providers.AllProviders() {
		assert.Equal(t, provider == providers.ProviderXAI, providers.OutputExcludesReasoning(provider), provider)
	}
}

func TestCredentialPrincipalIsAnEndpointSaltedOpaqueToken(t *testing.T) {
	const key = "sk-test-principal-key"
	first := providers.CredentialPrincipal("https://a.example", key)

	assert.Len(t, first, 16)
	assert.Equal(t, first, providers.CredentialPrincipal("https://a.example", key))
	assert.NotEqual(t, first, providers.CredentialPrincipal("https://b.example", key))
	assert.NotEqual(t, first, providers.CredentialPrincipal("https://a.example", key+"x"))
	assert.NotContains(t, first, key)
	assert.Empty(t, providers.CredentialPrincipal("https://a.example", ""))
}

func TestModelListNeedsEntityOnlyFor415And400(t *testing.T) {
	assert.True(t, providers.ModelListNeedsEntity(http.StatusUnsupportedMediaType))
	assert.True(t, providers.ModelListNeedsEntity(http.StatusBadRequest))
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		assert.False(t, providers.ModelListNeedsEntity(status), status)
	}
	assert.JSONEq(t, `{}`, string(providers.EmptyJSONEntity))
}

func TestCortexRESTRootMapsGatewayAndVersionedURLsWithoutCheckingTheHost(t *testing.T) {
	tests := map[string]string{
		"https://acct.snowflakecomputing.com/api/v2/cortex/v1":           "https://acct.snowflakecomputing.com/api/v2/cortex",
		"https://acct.snowflakecomputing.com/api/v2/aigateways/team/v1/": "https://acct.snowflakecomputing.com/api/v2/cortex",
		"https://proxy.example/api/v2/aigateways/team":                   "https://proxy.example/api/v2/cortex",
		"https://proxy.example/custom/v1":                                "https://proxy.example/custom",
	}
	for in, want := range tests {
		assert.Equal(t, want, providers.CortexRESTRoot(in), in)
	}
}

func TestStallSentinelsStayRetryableWhenTheWatchdogAlsoCancelledTheContext(t *testing.T) {
	for _, sentinel := range []error{
		providers.ErrUpstreamIdleTimeout,
		providers.ErrUpstreamOutputStall,
		providers.ErrUpstreamSlowThroughput,
		providers.ErrUpstreamEmptyCompletion,
	} {
		assert.True(t, providers.IsRetryable(errors.Join(sentinel, context.Canceled)), sentinel.Error())
	}
	assert.False(t, providers.IsRetryable(context.Canceled))
	assert.False(t, providers.IsRetryable(&providers.UpstreamStatusError{Status: http.StatusBadGateway}))
	assert.True(t, providers.IsRetryable(errors.New("dial tcp: connection refused")))
}

func TestBindingFatalStatusesGateCrossBindingFailoverOnly(t *testing.T) {
	notFound := &providers.UpstreamErrorResponse{Status: http.StatusNotFound}
	billing := &providers.UpstreamErrorResponse{Status: http.StatusPaymentRequired}

	assert.True(t, providers.IsUpstreamModelNotFound(notFound))
	assert.True(t, providers.IsUpstreamProviderBillingBlocked(billing))
	assert.False(t, providers.IsRetryable(notFound))
	assert.False(t, providers.IsRetryable(billing))
	assert.False(t, providers.IsUpstreamProviderBillingBlocked(notFound))
}

func TestKnownProviderErrorTypeAcceptsOnlyTheAllowlist(t *testing.T) {
	for _, known := range []string{"invalid_request_error", "authentication_error", "permission_error", "not_found_error", "rate_limit_error", "api_error", "overloaded_error", "service_unavailable"} {
		got, ok := providers.KnownProviderErrorType(known)
		require.True(t, ok, known)
		assert.Equal(t, known, string(got))
	}
	for _, unknown := range []string{"", "Invalid_Request_Error", "user said: secret", strings.Repeat("x", 64)} {
		_, ok := providers.KnownProviderErrorType(unknown)
		assert.False(t, ok, unknown)
	}
}
