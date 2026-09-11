package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestProxy_PreserveNativeCodexKeepsRequiredSubscriptionShaping: a preserved
// native Responses body still has to use the Codex backend, its account
// headers, and the unsupported-parameter strip, because the subscription
// credential authenticates nowhere else.
func TestProxy_PreserveNativeCodexKeepsRequiredSubscriptionShaping(t *testing.T) {
	var gotPath, gotAuth, gotAccount string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get(codexAccountIDHeader)
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer upstream.Close()

	c := NewClientWithModelIDMap("deployment-key", "https://api.example.invalid", map[string]string{"gpt-5.6-sol": "vendor-sol"})
	c.codexBaseURL = upstream.URL

	prep := providers.PreparedRequest{
		Body: []byte(`{"model":"gpt-5.6-sol","input":"hi","stream":true,"previous_response_id":"resp_prior",` +
			`"max_output_tokens":16000,"metadata":{"a":"b"},"parallel_tool_calls":true}`),
		Endpoint:       providers.EndpointResponses,
		Headers:        make(http.Header),
		PreserveNative: true,
	}
	err := c.Proxy(
		codexCtx("eyJhbGciOiJ-codex-jwt", "acct-12345"),
		router.Decision{Model: "gpt-5.6-sol", Provider: providers.ProviderOpenAI},
		prep,
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("")),
	)
	require.NoError(t, err)

	assert.Equal(t, codexResponsesPath, gotPath, "a Codex credential must still dispatch to the subscription endpoint")
	assert.Equal(t, "Bearer eyJhbGciOiJ-codex-jwt", gotAuth)
	assert.Equal(t, "acct-12345", gotAccount)
	assert.Equal(t, "gpt-5.6-sol", gjson.GetBytes(gotBody, "model").String(), "the original model spelling is preserved")
	assert.Equal(t, "resp_prior", gjson.GetBytes(gotBody, "previous_response_id").String(), "native Responses state survives")
	assert.True(t, gjson.GetBytes(gotBody, "parallel_tool_calls").Bool())
	for _, key := range codexUnsupportedParams {
		assert.False(t, gjson.GetBytes(gotBody, key).Exists(), "%s must still be stripped: the Codex backend 400s on it", key)
	}
}
