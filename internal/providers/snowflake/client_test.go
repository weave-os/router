package snowflake_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/providers/snowflake"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare account URL gets the Cortex prefix", "https://acct.snowflakecomputing.com", "https://acct.snowflakecomputing.com/api/v2/cortex"},
		{"trailing slash trimmed before appending", "https://acct.snowflakecomputing.com/", "https://acct.snowflakecomputing.com/api/v2/cortex"},
		{"already-Cortex URL untouched", "https://acct.snowflakecomputing.com/api/v2/cortex", "https://acct.snowflakecomputing.com/api/v2/cortex"},
		{"already-Cortex URL with trailing slash", "https://acct.snowflakecomputing.com/api/v2/cortex/", "https://acct.snowflakecomputing.com/api/v2/cortex"},
		{"empty stays empty so BYOK can supply it", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, snowflake.NormalizeBaseURL(tt.in))
		})
	}
}

// captured records what the fake Cortex endpoint saw, so tests can assert on
// the wire contract that differs from api.anthropic.com.
type captured struct {
	path   string
	auth   string
	apiKey string
}

func fakeCortex(t *testing.T, got *captured) *httptest.Server {
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

func TestProxy_SendsBearerPATToCortexMessagesEndpoint(t *testing.T) {
	var got captured
	upstream := fakeCortex(t, &got)

	// The operator configures the bare account URL; the adapter is responsible
	// for landing on Cortex's Anthropic-compatible Messages path.
	c := snowflake.NewClient("snowflake-pat", upstream.URL)
	prep, clientReq := messagesRequest()
	rec := httptest.NewRecorder()

	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-opus-5"}, prep, rec, clientReq))

	assert.Equal(t, snowflake.CortexPathPrefix+"/v1/messages", got.path)
	assert.Equal(t, "Bearer snowflake-pat", got.auth)
	assert.Empty(t, got.apiKey, "Cortex rejects x-api-key; the PAT must travel as a bearer token")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestProxy_UsesBYOKCredentialBaseURLAndToken(t *testing.T) {
	var got captured
	upstream := fakeCortex(t, &got)

	// Deployment has no Snowflake key at all: everything comes from the
	// installation's stored BYOK credential, whose base_url is its own account.
	c := snowflake.NewClient("", "")
	prep, clientReq := messagesRequest()
	ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
		APIKey:  []byte("byok-pat"),
		Source:  "byok",
		BaseURL: upstream.URL,
	})
	rec := httptest.NewRecorder()

	require.NoError(t, c.Proxy(ctx, router.Decision{Model: "claude-opus-5"}, prep, rec, clientReq))

	assert.Equal(t, snowflake.CortexPathPrefix+"/v1/messages", got.path,
		"a BYOK base URL must be normalized to the Cortex prefix too")
	assert.Equal(t, "Bearer byok-pat", got.auth)
	assert.Empty(t, got.apiKey)
}

func TestProxy_DoesNotRelayInboundAnthropicAuthToSnowflake(t *testing.T) {
	var got captured
	upstream := fakeCortex(t, &got)

	// Anthropic's adapter falls back to the caller's own credential; Snowflake
	// must not, since an Anthropic key or router key is meaningless to Cortex
	// and forwarding it leaks a credential across a tenant boundary.
	c := snowflake.NewClient("", upstream.URL)
	prep, clientReq := messagesRequest()
	clientReq.Header.Set("Authorization", "Bearer anthropic-oauth-token")
	clientReq.Header.Set("x-api-key", "sk-ant-customer-key")
	rec := httptest.NewRecorder()

	require.NoError(t, c.Proxy(context.Background(), router.Decision{Model: "claude-opus-5"}, prep, rec, clientReq))

	assert.Empty(t, got.auth)
	assert.Empty(t, got.apiKey)
}
