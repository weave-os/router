package codex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientProxyUsesCodexOAuthBackend(t *testing.T) {
	var gotPath, gotAuth, gotAccount string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-ID")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()

	client := NewClient(upstream.URL)
	ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
		APIKey:    []byte("chatgpt-oauth-token"),
		AccountID: []byte("acct-test"),
		Source:    "codex_subscription",
		OAuth:     true,
	})
	body := []byte(`{"model":"gpt-5.6-sol","input":"hello","stream":true}`)
	prep := providers.PreparedRequest{Body: body, Endpoint: providers.EndpointResponses, Headers: make(http.Header)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

	err := client.Proxy(ctx, router.Decision{Provider: providers.ProviderCodex, Model: "gpt-5.6-sol"}, prep, rec, req)

	require.NoError(t, err)
	assert.Equal(t, "/responses", gotPath)
	assert.Equal(t, "Bearer chatgpt-oauth-token", gotAuth)
	assert.Equal(t, "acct-test", gotAccount)
	assert.Equal(t, body, gotBody)
}

func TestClientRejectsNonOAuthCredential(t *testing.T) {
	client := NewClient("https://example.invalid")
	prep := providers.PreparedRequest{Body: []byte(`{"model":"gpt-5.6-sol"}`), Endpoint: providers.EndpointResponses}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

	err := client.Proxy(context.Background(), router.Decision{Provider: providers.ProviderCodex, Model: "gpt-5.6-sol"}, prep, httptest.NewRecorder(), req)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Codex OAuth credential")
}
