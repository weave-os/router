package anthropic_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

func TestFederatedCredentialUsesBearerWithoutSubscriptionBehavior(t *testing.T) {
	var headers http.Header
	var body []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"msg_test"}`))
	}))
	defer upstream.Close()
	client := anthropic.NewClient("deployment-key", upstream.URL)
	ctx := requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{
		APIKey:   []byte("sk-ant-oat01-synthetic-workload-token"),
		Source:   requestcontext.SourceBYOK,
		AuthType: auth.AuthTypeAnthropicWIF,
	})
	payload := `{"model":"claude-opus-5-5","system":"Keep this instruction","messages":[{"role":"user","content":"hello"}]}`
	err := client.Proxy(ctx, router.Decision{Model: "claude-opus-5-5"}, providers.PreparedRequest{
		Body: []byte(payload), Headers: make(http.Header),
	}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(payload)))
	require.NoError(t, err)
	assert.Equal(t, "Bearer sk-ant-oat01-synthetic-workload-token", headers.Get("Authorization"))
	assert.Empty(t, headers.Get("x-api-key"))
	assert.Empty(t, headers.Get("X-Snowflake-Authorization-Token-Type"))
	assert.Contains(t, headers.Get("anthropic-beta"), "oauth-2025-04-20")
	assert.NotContains(t, headers.Get("anthropic-beta"), "oidc-federation-2026-04-01")
	assert.JSONEq(t, payload, string(body))
}

func TestFederatedCredentialSupportsPassthroughAndModelListing(t *testing.T) {
	for _, listing := range []bool{false, true} {
		t.Run(map[bool]string{false: "passthrough", true: "model listing"}[listing], func(t *testing.T) {
			var headers http.Header
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers = r.Header.Clone()
				_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-5-5"}],"has_more":false}`))
			}))
			defer upstream.Close()
			client := anthropic.NewClient("deployment-key", upstream.URL, anthropic.WithModelListHTTPClient(anthropicDiscoveryClient(t, upstream.URL)))
			ctx := requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{APIKey: []byte("sk-ant-oat01-synthetic-workload-token"), AuthType: auth.AuthTypeAnthropicWIF, Source: requestcontext.SourceBYOK})
			if listing {
				models, err := client.ListModels(ctx)
				require.NoError(t, err)
				assert.Equal(t, []string{"claude-opus-5-5"}, models)
			} else {
				err := client.Passthrough(ctx, providers.PreparedRequest{Body: []byte(`{"model":"claude-opus-5-5"}`), Headers: make(http.Header)}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
				require.NoError(t, err)
			}
			assert.Equal(t, "Bearer sk-ant-oat01-synthetic-workload-token", headers.Get("Authorization"))
			assert.Empty(t, headers.Get("x-api-key"))
			assert.Empty(t, headers.Get(auth.WIFTokenTypeHeader))
			assert.Contains(t, headers.Get("anthropic-beta"), "oauth-2025-04-20")
			assert.NotContains(t, headers.Get("anthropic-beta"), "oidc-federation-2026-04-01")
		})
	}
}

func TestUnavailableFederationNeverUsesDeploymentKey(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	client := anthropic.NewClient("synthetic-platform-key", upstream.URL, anthropic.WithModelListHTTPClient(anthropicDiscoveryClient(t, upstream.URL)))
	ctx := requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{AuthType: auth.AuthTypeAnthropicWIF, CredentialError: auth.ErrAnthropicFederationUnavailable})
	prep := providers.PreparedRequest{Body: []byte(`{"model":"claude-opus-5-5"}`), Headers: make(http.Header)}
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.ErrorIs(t, client.Proxy(ctx, router.Decision{Model: "claude-opus-5-5"}, prep, httptest.NewRecorder(), request), auth.ErrAnthropicFederationUnavailable)
	require.ErrorIs(t, client.Passthrough(ctx, prep, httptest.NewRecorder(), request), auth.ErrAnthropicFederationUnavailable)
	_, err := client.ListModels(ctx)
	require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable)
	assert.Zero(t, upstreamCalls)
}
