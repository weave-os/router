package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/router"
)

func TestUnavailableFederationIsIsolatedToItsProvider(t *testing.T) {
	keys := []*auth.ExternalAPIKey{
		{ID: "federation-key", Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF, CredentialError: auth.ErrAnthropicFederationUnavailable},
		{ID: "openai-key", Provider: providers.ProviderOpenAI, Plaintext: []byte("synthetic-openai-key")},
	}
	ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, keys)
	model := "claude-opus-5-5"
	resolvedAnthropic := resolveAndInjectCredentials(ctx, providers.ProviderAnthropic, model, nil)
	anthropicCredential := CredentialsFromContext(resolvedAnthropic)
	require.NotNil(t, anthropicCredential)
	assert.ErrorIs(t, anthropicCredential.CredentialError, auth.ErrAnthropicFederationUnavailable)
	assert.Empty(t, anthropicCredential.APIKey)

	resolvedOpenAI := resolveAndInjectCredentials(ctx, providers.ProviderOpenAI, "gpt-5", nil)
	openAICredential := CredentialsFromContext(resolvedOpenAI)
	require.NotNil(t, openAICredential)
	assert.Equal(t, "synthetic-openai-key", string(openAICredential.APIKey))
	assert.NoError(t, openAICredential.CredentialError)

	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	client := anthropic.NewClient("synthetic-platform-key", upstream.URL)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	err := client.Proxy(resolvedAnthropic, router.Decision{Provider: providers.ProviderAnthropic, Model: model}, providers.PreparedRequest{Body: []byte(`{"model":"claude-opus-5-5"}`), Headers: make(http.Header)}, httptest.NewRecorder(), request)
	require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable)
	assert.Zero(t, upstreamCalls.Load(), "the platform Anthropic key must never be used as a fallback")
	classification, matched := ClassifyDispatchError(err)
	require.True(t, matched)
	assert.Equal(t, http.StatusServiceUnavailable, classification.Status)
	assert.True(t, classification.RetryAfter)
}

func TestFederationOutageDoesNotBlockOpenAIInference(t *testing.T) {
	upstream := &bypassFakeProvider{respBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-5.6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"served"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`}
	decision := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-luna", Reason: "test"}
	service := NewService(staticRouter{decision: decision}, map[string]providers.Client{providers.ProviderOpenAI: upstream}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-opus-4-8", nil)
	ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{ID: "federation-key", Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF, CredentialError: auth.ErrAnthropicFederationUnavailable},
		{ID: "openai-key", Provider: providers.ProviderOpenAI, Plaintext: []byte("synthetic-openai-key")},
	})
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	body := []byte(`{"model":"claude-opus-4-8","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`)
	recorder := httptest.NewRecorder()
	err := service.ProxyMessages(ctx, body, recorder, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	require.NoError(t, err)
	assert.Equal(t, 1, upstream.dispatches)
	require.NotNil(t, CredentialsFromContext(upstream.capturedCtx))
	assert.Equal(t, "synthetic-openai-key", string(CredentialsFromContext(upstream.capturedCtx).APIKey))
}

func TestFederationOutageFailsClosedOnAnthropicInference(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { upstreamCalls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	client := anthropic.NewClient("synthetic-platform-key", upstream.URL)
	decision := router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Reason: "test"}
	service := NewService(staticRouter{decision: decision}, map[string]providers.Client{providers.ProviderAnthropic: client}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-opus-4-8", nil)
	ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{{ID: "federation-key", Provider: providers.ProviderAnthropic, AuthType: auth.AuthTypeAnthropicWIF, CredentialError: auth.ErrAnthropicFederationUnavailable}})
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	body := []byte(`{"model":"claude-opus-4-8","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`)
	err := service.ProxyMessages(ctx, body, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	require.ErrorIs(t, err, auth.ErrAnthropicFederationUnavailable)
	classification, matched := ClassifyDispatchError(err)
	require.True(t, matched)
	assert.Equal(t, http.StatusServiceUnavailable, classification.Status)
	assert.Zero(t, upstreamCalls.Load())
}
