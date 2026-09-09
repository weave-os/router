package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

type purposeSink struct{ events []inference.AttemptEvent }

func (s *purposeSink) RecordAttempt(_ context.Context, e inference.AttemptEvent) {
	s.events = append(s.events, e)
}

func jsonUpstream(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

// Every public surface must reach the provider through a resolved plan: the
// executor stamps the surface's purpose and policy onto each attempt, which is
// what the telemetry and policy inventory join on.
func TestPublicSurfaces_DispatchThroughResolvedPlan(t *testing.T) {
	cases := map[string]struct {
		provider string
		model    string
		purpose  inference.Purpose
		policyID inference.PolicyID
		run      func(*proxy.Service, http.ResponseWriter) error
		upstream func(http.ResponseWriter)
	}{
		"anthropic messages": {
			provider: providers.ProviderAnthropic, model: "claude-haiku-4-5",
			purpose: inference.PurposeAnthropicMessages, policyID: "main-anthropic-messages",
			upstream: jsonUpstream(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}`),
			run: func(svc *proxy.Service, w http.ResponseWriter) error {
				body := []byte(`{"model":"claude-haiku-4-5","max_tokens":4096,"messages":[{"role":"user","content":"hi"}]}`)
				return svc.ProxyMessages(authedCtx("00000000-0000-0000-0000-000000000001"), body, w,
					httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("")))
			},
		},
		"openai chat completions": {
			provider: providers.ProviderOpenAI, model: "gpt-5.6-luna",
			purpose: inference.PurposeOpenAIChatCompletions, policyID: "main-openai-chat-completions",
			upstream: jsonUpstream(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`),
			run: func(svc *proxy.Service, w http.ResponseWriter) error {
				body := []byte(`{"model":"auto","stream":false,"messages":[{"role":"user","content":"hi"}],"stop":["END"]}`)
				return svc.ProxyOpenAIChatCompletion(authedCtx("00000000-0000-0000-0000-000000000001"), body, w,
					httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("")))
			},
		},
		"openai responses": {
			provider: providers.ProviderOpenAI, model: "gpt-5.5",
			purpose: inference.PurposeOpenAIResponses, policyID: "main-openai-responses",
			upstream: jsonUpstream(`{"id":"resp_1","object":"response","output":[]}`),
			run: func(svc *proxy.Service, w http.ResponseWriter) error {
				body := []byte(`{"model":"gpt-5.5","input":"hi"}`)
				return svc.ProxyOpenAIResponses(context.Background(), body, w,
					httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("")))
			},
		},
		"gemini generate content": {
			provider: providers.ProviderGoogle, model: "gemini-2.5-pro",
			purpose: inference.PurposeGeminiGenerateContent, policyID: "main-gemini-generate-content",
			upstream: jsonUpstream(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}]}`),
			run: func(svc *proxy.Service, w http.ResponseWriter) error {
				return svc.ProxyGeminiGenerateContent(authedCtx("00000000-0000-0000-0000-000000000001"), []byte(geminiInjectedBody), w,
					httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-1.5-pro:generateContent", strings.NewReader("")))
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: tc.upstream}
			clients := map[string]providers.Client{tc.provider: provider}
			sink := &purposeSink{}
			executor, err := dispatch.NewExecutor(dispatch.NewClients(clients), dispatch.WithAttemptSink(sink))
			require.NoError(t, err)
			fr := &fakeRouter{decision: router.Decision{Provider: tc.provider, Model: tc.model, Reason: "test"}}
			svc := proxy.NewService(fr, clients, nil, false, nil, newFakePinStore(), false, tc.provider, tc.model, nil).
				WithInferenceExecutor(executor)

			rec := httptest.NewRecorder()
			require.NoError(t, tc.run(svc, rec))

			require.Len(t, provider.proxyBodies, 1)
			require.Len(t, sink.events, 1)
			event := sink.events[0]
			assert.Equal(t, tc.purpose, event.Purpose)
			assert.Equal(t, tc.policyID, event.PolicyID)
			assert.NotEmpty(t, event.RegistryRevision)
			assert.Equal(t, inference.AttemptOutcomeServed, event.Outcome)
			assert.Equal(t, tc.model, event.Target.CatalogID)
			assert.Equal(t, tc.provider, event.Target.Provider)
		})
	}
}

// A hard-pinned utility turn is authorized under its own purpose and policy
// and served on the deployment hard pin — not under the ingress surface's
// main-inference policy.
func TestHardPinnedUtilityTurns_DispatchUnderOwnPurpose(t *testing.T) {
	const anthropicOK = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	cases := map[string]struct {
		body     string
		purpose  inference.Purpose
		policyID inference.PolicyID
	}{
		"probe": {
			body:    `{"model":"claude-sonnet-4-6","max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`,
			purpose: inference.PurposeProbe, policyID: "aux-probe",
		},
		"classifier": {
			body:    `{"model":"claude-sonnet-4-6","max_tokens":64,"messages":[{"role":"user","content":"is this safe? yes/no"}]}`,
			purpose: inference.PurposeClassifier, policyID: "aux-classifier",
		},
		"title generation": {
			body:    `{"model":"claude-sonnet-4-6","max_tokens":512,"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"title":{"type":"string"}}}}},"messages":[{"role":"user","content":"Please write a title for this conversation."}]}`,
			purpose: inference.PurposeTitleGeneration, policyID: "aux-title-generation",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: jsonUpstream(anthropicOK)}
			clients := map[string]providers.Client{providers.ProviderAnthropic: provider}
			sink := &purposeSink{}
			executor, err := dispatch.NewExecutor(dispatch.NewClients(clients), dispatch.WithAttemptSink(sink))
			require.NoError(t, err)
			fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-4-6", Reason: "test"}}
			svc := proxy.NewService(fr, clients, nil, false, nil, newFakePinStore(), false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
				WithInferenceExecutor(executor)

			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyMessages(authedCtx("00000000-0000-0000-0000-000000000001"), []byte(tc.body), rec,
				httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

			require.Len(t, provider.proxyBodies, 1)
			require.Len(t, sink.events, 1)
			event := sink.events[0]
			assert.Equal(t, tc.purpose, event.Purpose)
			assert.Equal(t, tc.policyID, event.PolicyID)
			assert.Equal(t, inference.AttemptOutcomeServed, event.Outcome)
			assert.Equal(t, "claude-haiku-4-5", event.Target.CatalogID)
			assert.Equal(t, providers.ProviderAnthropic, event.Target.Provider)
		})
	}
}
