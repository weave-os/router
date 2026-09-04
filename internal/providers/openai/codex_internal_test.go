package openai

import (
	"context"
	"io"
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

func codexCtx(token, accountID string) context.Context {
	return context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
		APIKey:    []byte(token),
		AccountID: []byte(accountID),
		Source:    "codex_subscription",
		OAuth:     true,
	})
}

// TestProxy_CodexSubscriptionDispatch verifies a Codex (ChatGPT) subscription
// credential reroutes the upstream call to the Codex backend's /responses
// endpoint with the required auth + account-id + beta + originator headers, and
// forwards the prepared Responses body byte-for-byte.
func TestProxy_CodexSubscriptionDispatch(t *testing.T) {
	var gotPath, gotAuth, gotAccount, gotBeta, gotOriginator string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-ID")
		gotBeta = r.Header.Get("OpenAI-Beta")
		gotOriginator = r.Header.Get("originator")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()

	c := NewClient("deployment-key", upstream.URL)
	c.codexBaseURL = upstream.URL // point the Codex backend at the test server

	body := []byte(`{"model":"gpt-5.6-sol","input":"hi","stream":true}`)
	prep := providers.PreparedRequest{Body: body, Endpoint: providers.EndpointResponses, Headers: make(http.Header)}
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

	ctx := codexCtx("eyJhbGciOiJ-codex-jwt", "acct-12345")
	err := c.Proxy(ctx, router.Decision{Model: "gpt-5.6-sol", Provider: providers.ProviderOpenAI}, prep, rec, clientReq)
	require.NoError(t, err)

	assert.Equal(t, "/responses", gotPath, "a Codex subscription turn must hit the Codex backend's /responses endpoint, not api.openai.com")
	assert.Equal(t, "Bearer eyJhbGciOiJ-codex-jwt", gotAuth, "the ChatGPT OAuth JWT must be sent as a Bearer token")
	assert.Equal(t, "acct-12345", gotAccount, "the ChatGPT-Account-ID header is required by the Codex backend")
	assert.Equal(t, "responses=experimental", gotBeta)
	assert.Equal(t, "codex_cli_rs", gotOriginator)
	assert.Empty(t, rec.Header().Get("x-api-key"))
	assert.Equal(t, body, gotBody, "the prepared Responses body must reach the Codex backend unchanged")
}

// TestProxy_CodexSubscriptionStripsUnsupportedParams verifies the params the
// ChatGPT backend 400s on ("Unsupported parameter: max_output_tokens") are
// dropped before dispatch, since a translated turn — Anthropic ingress always
// carries max_tokens — legitimately emits them.
func TestProxy_CodexSubscriptionStripsUnsupportedParams(t *testing.T) {
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()

	c := NewClient("deployment-key", upstream.URL)
	c.codexBaseURL = upstream.URL

	prep := providers.PreparedRequest{
		Body: []byte(`{"model":"gpt-5.6-sol","input":"hi","stream":true,"max_output_tokens":16000,` +
			`"temperature":1,"top_p":1,"metadata":{"a":"b"},"service_tier":"auto","truncation":"auto",` +
			`"parallel_tool_calls":true}`),
		Endpoint: providers.EndpointResponses,
		Headers:  make(http.Header),
	}
	err := c.Proxy(
		codexCtx("eyJhbGciOiJ-codex-jwt", "acct-12345"),
		router.Decision{Model: "gpt-5.6-sol", Provider: providers.ProviderOpenAI},
		prep,
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("")),
	)
	require.NoError(t, err)

	for _, key := range codexUnsupportedParams {
		assert.False(t, gjson.GetBytes(gotBody, key).Exists(),
			"%s must be stripped: the Codex backend 400s on it", key)
	}
	assert.True(t, gjson.GetBytes(gotBody, "parallel_tool_calls").Bool(),
		"supported params must survive")
	assert.Equal(t, "hi", gjson.GetBytes(gotBody, "input").String(), "the turn itself must be untouched")
}

func TestProxy_CodexSubscriptionCredentialRejectsInfrastructureModel(t *testing.T) {
	c := NewClient("deployment-key", "https://api.openai.example")
	prep := providers.PreparedRequest{
		Body:     []byte(`{"model":"gpt-5.4-nano","input":"hi"}`),
		Endpoint: providers.EndpointResponses,
		Headers:  make(http.Header),
	}
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

	err := c.Proxy(
		codexCtx("eyJhbGciOiJ-codex-jwt", "acct-12345"),
		router.Decision{Model: "gpt-5.4-nano", Provider: providers.ProviderOpenAI},
		prep,
		rec,
		clientReq,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing Codex subscription credential")
}

// TestProxy_CodexCredOnChatEndpointDoesNotMisroute guards the Bugbot finding:
// the Codex backend only accepts the Responses schema, so a chat-completions
// prep that happens to resolve a Codex credential must NOT be posted to the
// Codex /responses endpoint. The switch is gated on EndpointResponses.
func TestProxy_CodexCredOnChatEndpointDoesNotMisroute(t *testing.T) {
	var gotPath, gotAccount string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccount = r.Header.Get("ChatGPT-Account-ID")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	c := NewClient("deployment-key", upstream.URL)
	c.codexBaseURL = "https://chatgpt.example.invalid" // must NOT be used for a chat body

	// EndpointChatCompletions (zero value) — a chat-shaped body.
	prep := providers.PreparedRequest{Body: []byte(`{"model":"gpt-5.6-sol","messages":[]}`), Headers: make(http.Header)}
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))

	ctx := codexCtx("eyJhbGciOiJ-codex-jwt", "acct-12345")
	err := c.Proxy(ctx, router.Decision{Model: "gpt-5.6-sol", Provider: providers.ProviderOpenAI}, prep, rec, clientReq)
	require.NoError(t, err)

	assert.Equal(t, "/v1/chat/completions", gotPath,
		"a chat-completions body must never be posted to the Codex /responses endpoint, even with a Codex credential")
	assert.Empty(t, gotAccount, "the Codex account-id header must not be set on a non-Responses dispatch")
}

// TestProxy_NoCodexCredHitsOpenAI confirms the Codex switch is gated on the
// subscription credential: a normal (deployment-key) request still targets
// api.openai.com and sends no ChatGPT-Account-ID header.
func TestProxy_NoCodexCredHitsOpenAI(t *testing.T) {
	var gotPath, gotAccount string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccount = r.Header.Get("ChatGPT-Account-ID")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	c := NewClient("deployment-key", upstream.URL)
	c.codexBaseURL = "https://chatgpt.example.invalid" // must NOT be used

	prep := providers.PreparedRequest{Body: []byte(`{"model":"gpt-5.5"}`), Headers: make(http.Header)}
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))

	err := c.Proxy(context.Background(), router.Decision{Model: "gpt-5.5", Provider: providers.ProviderOpenAI}, prep, rec, clientReq)
	require.NoError(t, err)

	assert.Equal(t, "/v1/chat/completions", gotPath)
	assert.Empty(t, gotAccount, "a non-subscription request must not send the Codex account-id header")
}

// TestProxy_ResponsesMaxEffortMatchesPublicModelMenu guards the per-model
// distinction: GPT-5.6 rejects max publicly, while GPT-6 Astra accepts it.
func TestProxy_ResponsesMaxEffortMatchesPublicModelMenu(t *testing.T) {
	for _, tc := range []struct {
		model      string
		wantEffort string
	}{
		{model: "gpt-5.6-sol", wantEffort: "xhigh"},
		{model: "gpt-6-astra", wantEffort: "max"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			var gotPath string
			var gotBody []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
			}))
			defer upstream.Close()

			c := NewClient("deployment-key", upstream.URL)
			c.codexBaseURL = "https://chatgpt.example.invalid"
			body := []byte(`{"model":"` + tc.model + `","input":"hi","stream":true,"reasoning":{"effort":"max","summary":"detailed"}}`)
			prep := providers.PreparedRequest{Body: body, Endpoint: providers.EndpointResponses, Headers: make(http.Header)}
			err := c.Proxy(
				context.Background(),
				router.Decision{Model: tc.model, Provider: providers.ProviderOpenAI},
				prep,
				httptest.NewRecorder(),
				httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("")),
			)
			require.NoError(t, err)

			assert.Equal(t, "/v1/responses", gotPath)
			assert.Equal(t, tc.wantEffort, gjson.GetBytes(gotBody, "reasoning.effort").String())
			assert.Equal(t, "detailed", gjson.GetBytes(gotBody, "reasoning.summary").String())
		})
	}
}

// TestProxy_ResponsesMaxEffortUnchangedWithCodexCred confirms the clamp is
// scoped to the non-Codex-backend branch; the Codex backend accepts "max" natively.
func TestProxy_ResponsesMaxEffortUnchangedWithCodexCred(t *testing.T) {
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()

	c := NewClient("deployment-key", upstream.URL)
	c.codexBaseURL = upstream.URL

	body := []byte(`{"model":"gpt-5.6-sol","input":"hi","stream":true,"reasoning":{"effort":"max","summary":"detailed"}}`)
	prep := providers.PreparedRequest{Body: body, Endpoint: providers.EndpointResponses, Headers: make(http.Header)}
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

	ctx := codexCtx("eyJhbGciOiJ-codex-jwt", "acct-12345")
	err := c.Proxy(ctx, router.Decision{Model: "gpt-5.6-sol", Provider: providers.ProviderOpenAI}, prep, rec, clientReq)
	require.NoError(t, err)

	assert.Equal(t, body, gotBody, "the Codex backend understands 'max' natively; the body must not be altered")
}
