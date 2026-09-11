package openai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const preservedResponsesBody = `{"model":"gpt-5.6-luna","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],` +
	`"previous_response_id":"resp_prior","reasoning":{"effort":"high","summary":"auto"},"store":false,"include":["reasoning.encrypted_content"],` +
	`"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}],"unknown_native_field":{"kept":true},"stream":true}`

func TestProxy_PreserveNativeForwardsResponsesFieldsVerbatim(t *testing.T) {
	var (
		gotPath string
		gotAuth string
		gotBody []byte
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()

	c := openai.NewClientWithModelIDMap("deployment-key", upstream.URL, map[string]string{"gpt-5.6-luna": "vendor-luna"})
	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	prep := providers.PreparedRequest{
		Body:           []byte(preservedResponsesBody),
		Headers:        make(http.Header),
		Endpoint:       providers.EndpointResponses,
		PreserveNative: true,
	}

	err := c.Proxy(context.Background(), router.Decision{Model: "gpt-5.6-luna", Provider: providers.ProviderOpenAI}, prep, rec, clientReq)
	require.NoError(t, err)

	assert.Equal(t, "/v1/responses", gotPath, "the native Responses request must stay on the Responses endpoint")
	assert.Equal(t, "Bearer deployment-key", gotAuth)
	assert.JSONEq(t, preservedResponsesBody, string(gotBody), "previous_response_id, reasoning, include and unknown fields must reach OpenAI unchanged")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "response.completed")
}

func TestProxy_PreserveNativeKeepsChatCompletionsModelSpelling(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &gotBody))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-native","object":"chat.completion"}`))
	}))
	defer upstream.Close()

	c := openai.NewClientWithModelIDMap("deployment-key", upstream.URL, map[string]string{"gpt-5.6-luna-pro": "gpt-5.6-luna"})
	prep := providers.PreparedRequest{
		Body:           []byte(`{"model":"gpt-5.6-luna-pro","messages":[{"role":"user","content":"hi"}]}`),
		Headers:        make(http.Header),
		PreserveNative: true,
	}
	err := c.Proxy(context.Background(), router.Decision{Model: "gpt-5.6-luna-pro"}, prep, httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("")))
	require.NoError(t, err)

	assert.Equal(t, "/v1/chat/completions", gotPath)
	assert.Equal(t, "gpt-5.6-luna-pro", gotBody["model"], "the caller's own model spelling is preserved")
}

func TestProxy_PreserveNativeStillHonorsBYOKAlias(t *testing.T) {
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &gotBody))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_alias","object":"response"}`))
	}))
	defer upstream.Close()

	c := openai.NewClient("deployment-key", upstream.URL)
	ctx := context.WithValue(context.Background(), requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey:       []byte("byok-key"),
		Source:       "byok",
		ModelAliases: map[string]string{"gpt-5.6-luna": "tenant-luna"},
	})
	prep := providers.PreparedRequest{Body: []byte(preservedResponsesBody), Headers: make(http.Header), Endpoint: providers.EndpointResponses, PreserveNative: true}
	err := c.Proxy(ctx, router.Decision{Model: "gpt-5.6-luna"}, prep, httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("")))
	require.NoError(t, err)
	assert.Equal(t, "tenant-luna", gotBody["model"], "the BYOK endpoint alias is a provider-required contract and still applies")
	assert.Equal(t, "resp_prior", gotBody["previous_response_id"])
}
