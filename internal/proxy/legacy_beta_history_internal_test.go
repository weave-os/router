package proxy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Sessions that predate the /beta retirement still replay its command turns
// and acknowledgements. These pin that they never reach routing or upstream.

func legacyBetaJSONQuote(t *testing.T, value string) string {
	t.Helper()
	quoted, err := json.Marshal(value)
	require.NoError(t, err)
	return string(quoted)
}

func TestProxyEntrypointsStripHistoricalBetaArtifactsBeforeRouting(t *testing.T) {
	for _, tt := range []struct {
		name   string
		openAI bool
		tools  []any
	}{
		{
			name: "anthropic",
			tools: []any{
				map[string]any{"name": "Read", "input_schema": map[string]any{"type": "object"}},
			},
		},
		{
			name:   "openai",
			openAI: true,
			tools: []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "Read", "parameters": map[string]any{"type": "object"}}},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			decisionProvider := providers.ProviderAnthropic
			decisionModel := "claude-haiku-4-5"
			if tt.openAI {
				decisionProvider = providers.ProviderOpenAI
				decisionModel = "gpt-5.5"
			}
			routing := &captureTestRouter{decision: router.Decision{
				Provider: decisionProvider,
				Model:    decisionModel,
				Reason:   "test",
			}}
			svc := withHMMPorts(NewService(
				routing,
				dispatch.NewClients(map[string]providers.Client{
					providers.ProviderAnthropic: embedTestProvider{},
					providers.ProviderOpenAI:    embedTestProvider{},
				}),
				nil, nil, false,
				providers.ProviderAnthropic, "claude-haiku-4-5", nil))

			body, err := json.Marshal(map[string]any{
				"model": "claude-opus-4-8",
				"messages": []any{
					map[string]any{"role": "user", "content": "inspect this repository"},
					map[string]any{"role": "assistant", "content": "I will inspect it."},
					map[string]any{"role": "user", "content": "/beta"},
					map[string]any{"role": "assistant", "content": "✦ **Weave Router** → Beta enabled. Type /beta again to turn it off.\n\n"},
					map[string]any{"role": "user", "content": "continue with the implementation"},
				},
				"tools":      tt.tools,
				"max_tokens": 4096,
			})
			require.NoError(t, err)
			request := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/messages", nil)
			response := httptest.NewRecorder()
			if tt.openAI {
				request = httptest.NewRequestWithContext(t.Context(), "POST", "/v1/chat/completions", nil)
				err = svc.ProxyOpenAIChatCompletion(context.Background(), body, response, request)
			} else {
				err = svc.ProxyMessages(context.Background(), body, response, request)
			}
			require.NoError(t, err)
			require.Len(t, routing.requests, 1)
			assert.NotContains(t, routing.requests[0].PromptText, "/beta")
			assert.NotContains(t, routing.requests[0].PromptText, "Beta enabled")
			assert.Contains(t, routing.requests[0].PromptText, "continue with the implementation")
			assert.Len(t, routing.requests[0].ConversationMessages, 3,
				"the command and acknowledgement must both be absent from routing features")
		})
	}
}

func TestHistoricalBetaArtifactsStripStaleThinkingSignatures(t *testing.T) {
	for _, tt := range []struct {
		name string
		ack  string
	}{
		{name: "after enabling beta", ack: "✦ **Weave Router** → Beta enabled. Type /beta again to turn it off.\n\n"},
		{name: "after restoring stable", ack: "✦ **Weave Router** → Beta disabled. Stable routing restored.\n\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			routing := &captureTestRouter{decision: router.Decision{
				Provider: providers.ProviderAnthropic,
				Model:    "claude-opus-4-7",
				Reason:   "test",
			}}
			provider := &captureTestProvider{}
			svc := withHMMPorts(NewService(
				routing,
				dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: provider}),
				nil, nil, false,
				providers.ProviderAnthropic, "claude-opus-4-7", nil))

			body := []byte(`{
				"model":"claude-opus-4-7",
				"messages":[
					{"role":"user","content":"inspect this repository"},
					{"role":"assistant","content":[
						{"type":"thinking","thinking":"old thought","signature":"stale-signature"},
						{"type":"text","text":"I will inspect it."}
					]},
					{"role":"user","content":"/beta"},
					{"role":"assistant","content":` + legacyBetaJSONQuote(t, tt.ack) + `},
					{"role":"user","content":"continue with the implementation"}
				],
				"max_tokens":4096,
				"thinking":{"type":"adaptive"}
			}`)
			request := httptest.NewRequestWithContext(t.Context(), "POST", "/v1/messages", nil)
			response := httptest.NewRecorder()

			require.NoError(t, svc.ProxyMessages(context.Background(), body, response, request))
			require.NotEmpty(t, provider.body)
			assert.NotContains(t, string(provider.body), "stale-signature")
			assert.NotContains(t, string(provider.body), `"type":"thinking"`)
			assert.NotContains(t, string(provider.body), "/beta")
			assert.Contains(t, string(provider.body), "continue with the implementation")
		})
	}
}
