package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestProxyOpenAIResponses_ForcedOpusAssistantContinuation(t *testing.T) {
	var upstreamBodies [][]byte
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		upstreamBodies = append(upstreamBodies, body)
		mu.Unlock()
		messages := gjson.GetBytes(body, "messages").Array()
		if len(messages) == 0 || messages[len(messages)-1].Get("role").String() != string(translate.EscalationRoleUser) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"This model does not support assistant message prefill. The conversation must end with a user message."}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.NewReplacer("rescued", "continued", "claude-sonnet-5", "claude-opus-5-5").Replace(anthropicRescueSSE))
	}))
	t.Cleanup(upstream.Close)
	svc := proxy.NewService(&fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic, Model: "claude-opus-5-5", Reason: translate.ReasonUserForceModel,
	}}, map[string]providers.Client{
		providers.ProviderAnthropic: anthropic.NewClient("test-key", upstream.URL),
	}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-opus-5-5", nil)
	ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
	body := []byte(`{"model":"gpt-6.1-sol","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Inspect the project"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I will inspect its tests next."}]}]}`)
	sink := httptest.NewRecorder()
	err := svc.ProxyOpenAIResponses(ctx, body, sink, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))

	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, upstreamBodies, 1, "continue on the explicitly selected model without rescue")
	assert.Equal(t, "claude-opus-5-5", gjson.GetBytes(upstreamBodies[0], "model").String())
	assert.Equal(t, "I will inspect its tests next.", gjson.GetBytes(upstreamBodies[0], "messages.1.content.0.text").String())
	assert.Contains(t, sink.Body.String(), "continued")
	assert.Contains(t, sink.Body.String(), "response.completed")
	assert.NotContains(t, sink.Body.String(), "response.failed")
}
