package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// syntheticCodexBackend enforces the request-shape rules the ChatGPT Codex
// backend applies that the public Responses API does not: a list `input`,
// `store: false`, and no `system` role items.
func syntheticCodexBackend(t *testing.T, received *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSyntheticCodexQuota(w, r) {
			return
		}
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		*received = body
		reject := func(detail string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"detail":"`+detail+`"}`)
		}
		input := gjson.GetBytes(body, "input")
		if !input.IsArray() {
			reject("Input must be a list")
			return
		}
		if store := gjson.GetBytes(body, "store"); store.Type != gjson.False {
			reject("Store must be set to false")
			return
		}
		for _, item := range input.Array() {
			if item.Get("role").String() == "system" {
				reject("System messages are not allowed")
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
}

func TestProxy_CodexSubscriptionShapesPublicResponsesRequests(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		check func(t *testing.T, sent []byte)
	}{
		{
			name: "string input",
			body: `{"model":"gpt-6-luna","store":false,"input":"Say ok."}`,
			check: func(t *testing.T, sent []byte) {
				t.Helper()
				assert.JSONEq(t, `[{"type":"message","role":"user","content":[{"type":"input_text","text":"Say ok."}]}]`,
					gjson.GetBytes(sent, "input").Raw)
			},
		},
		{
			name: "list input without store",
			body: `{"model":"gpt-6-luna","input":[{"role":"user","content":[{"type":"input_text","text":"Say ok."}]}]}`,
			check: func(t *testing.T, sent []byte) {
				t.Helper()
				assert.JSONEq(t, `[{"role":"user","content":[{"type":"input_text","text":"Say ok."}]}]`,
					gjson.GetBytes(sent, "input").Raw)
			},
		},
		{
			name: "store true",
			body: `{"model":"gpt-6-luna","store":true,"input":[{"role":"user","content":"Say ok."}]}`,
		},
		{
			name: "system role items",
			body: `{"model":"gpt-6-luna","store":false,"input":[{"role":"system","content":"Be terse."},{"type":"message","role":"system","content":[{"type":"input_text","text":"No markdown."}]},{"role":"user","content":"Say ok."}]}`,
			check: func(t *testing.T, sent []byte) {
				t.Helper()
				assert.Equal(t, []string{"developer", "developer", "user"}, roles(sent))
				assert.Equal(t, "Be terse.", gjson.GetBytes(sent, "input.0.content").String())
				assert.Equal(t, "No markdown.", gjson.GetBytes(sent, "input.1.content.0.text").String())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent []byte
			upstream := syntheticCodexBackend(t, &sent)
			defer upstream.Close()
			client := NewClient("deployment-key", "https://api.openai.example.invalid")
			client.SetCodexBaseURL(upstream.URL)
			prepared := providers.PreparedRequest{Body: []byte(tc.body), Endpoint: providers.EndpointResponses, Headers: make(http.Header)}

			err := client.Proxy(codexCtx("synthetic-codex-jwt", "synthetic-account"),
				router.Decision{Model: "gpt-6-luna", Provider: providers.ProviderOpenAI},
				prepared, httptest.NewRecorder(),
				httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", nil))

			require.NoError(t, err)
			assert.Equal(t, gjson.False, gjson.GetBytes(sent, "store").Type)
			if tc.check != nil {
				tc.check(t, sent)
			}
		})
	}
}

func TestProxy_CodexSubscriptionLeavesCodexShapedBodyByteIdentical(t *testing.T) {
	body := `{"model":"gpt-6-luna","instructions":"x","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"d"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"u"}]}],"store":false,"stream":true}`
	var sent []byte
	upstream := syntheticCodexBackend(t, &sent)
	defer upstream.Close()
	client := NewClient("deployment-key", "https://api.openai.example.invalid")
	client.SetCodexBaseURL(upstream.URL)

	err := client.Proxy(codexCtx("synthetic-codex-jwt", "synthetic-account"),
		router.Decision{Model: "gpt-6-luna", Provider: providers.ProviderOpenAI},
		providers.PreparedRequest{Body: []byte(body), Endpoint: providers.EndpointResponses, Headers: make(http.Header)},
		httptest.NewRecorder(),
		httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", nil))

	require.NoError(t, err)
	assert.Equal(t, body, string(sent))
}

func TestProxy_APIKeyResponsesKeepsPublicShape(t *testing.T) {
	body := `{"model":"gpt-6-luna","input":"Say ok.","stream":true}`
	var sent []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()
	client := NewClient("deployment-key", upstream.URL)

	err := client.Proxy(t.Context(),
		router.Decision{Model: "gpt-6-luna", Provider: providers.ProviderOpenAI},
		providers.PreparedRequest{Body: []byte(body), Endpoint: providers.EndpointResponses, Headers: make(http.Header)},
		httptest.NewRecorder(),
		httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", nil))

	require.NoError(t, err)
	assert.Equal(t, body, string(sent))
}

func roles(body []byte) []string {
	var out []string
	for _, item := range gjson.GetBytes(body, "input").Array() {
		out = append(out, item.Get("role").String())
	}
	return out
}
