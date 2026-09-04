package anthropic_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captured records what the fake upstream saw, so tests can assert on the wire
// contract that differs from api.anthropic.com.
type captured struct {
	path   string
	auth   string
	apiKey string
}

func fakeGateway(t *testing.T, got *captured) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization")
		got.apiKey = r.Header.Get("x-api-key")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func messagesRequest() (providers.PreparedRequest, *http.Request) {
	prep := providers.PreparedRequest{
		Body:    []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`),
		Headers: make(http.Header),
	}
	return prep, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
}

func TestBearerScheme_SendsDeploymentTokenAsBearer(t *testing.T) {
	var got captured
	upstream := fakeGateway(t, &got)

	c := anthropic.NewClient("gateway-token", upstream.URL, anthropic.WithAuthScheme(anthropic.AuthBearer))
	prep, clientReq := messagesRequest()
	rec := httptest.NewRecorder()

	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-opus-5"}, prep, rec, clientReq))

	assert.Equal(t, "/v1/messages", got.path, "the configured base URL is used verbatim")
	assert.Equal(t, "Bearer gateway-token", got.auth)
	assert.Empty(t, got.apiKey, "a Bearer-scheme gateway rejects x-api-key")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestBearerScheme_UsesBYOKCredentialBaseURLAndToken(t *testing.T) {
	var got captured
	upstream := fakeGateway(t, &got)

	// Deployment holds no gateway key: token and endpoint both come from the
	// installation's stored BYOK credential.
	c := anthropic.NewClient("", "", anthropic.WithAuthScheme(anthropic.AuthBearer))
	prep, clientReq := messagesRequest()
	ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
		APIKey:  []byte("byok-token"),
		Source:  "byok",
		BaseURL: upstream.URL,
	})
	rec := httptest.NewRecorder()

	require.NoError(t, c.Proxy(ctx, router.Decision{Model: "claude-opus-5"}, prep, rec, clientReq))

	assert.Equal(t, "Bearer byok-token", got.auth)
	assert.Empty(t, got.apiKey)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestBearerScheme_DoesNotRelayInboundAnthropicAuth(t *testing.T) {
	var got captured
	upstream := fakeGateway(t, &got)

	// A Bearer gateway must not relay the caller's Anthropic key — it is
	// meaningless there and leaks a credential across a tenant boundary.
	c := anthropic.NewClient("", upstream.URL, anthropic.WithAuthScheme(anthropic.AuthBearer))
	prep, clientReq := messagesRequest()
	clientReq.Header.Set("Authorization", "Bearer anthropic-oauth-token")
	clientReq.Header.Set("x-api-key", "sk-ant-customer-key")
	rec := httptest.NewRecorder()

	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-opus-5"}, prep, rec, clientReq))

	assert.Empty(t, got.auth)
	assert.Empty(t, got.apiKey)
}

func TestBearerScheme_EmptyBaseURLDoesNotFallBackToAnthropic(t *testing.T) {
	// An unconfigured Bearer gateway must not inherit DefaultBaseURL: that would
	// ship a third party's token to api.anthropic.com.
	c := anthropic.NewClient("gateway-token", "", anthropic.WithAuthScheme(anthropic.AuthBearer))
	prep, clientReq := messagesRequest()

	err := c.Proxy(context.Background(), router.Decision{Model: "claude-opus-5"}, prep, httptest.NewRecorder(), clientReq)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "api.anthropic.com")
}
