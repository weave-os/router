package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

func TestHeldObservationLanesPreserveInferenceResponses(t *testing.T) {
	for _, tc := range []struct {
		name, provider, model, path, request, response string
		call                                           func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"anthropic", providers.ProviderAnthropic, "claude-haiku-4-5", "/v1/messages", `{"model":"claude-haiku-4-5","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`, `{"id":"msg_test","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":4}}`, (*proxy.Service).ProxyMessages},
		{"chat", providers.ProviderOpenAI, "gpt-5.5", "/v1/chat/completions", `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`, `{"id":"chat_test","object":"chat.completion","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}}`, (*proxy.Service).ProxyOpenAIChatCompletion},
		{"responses", providers.ProviderOpenAI, "gpt-5.5", "/v1/responses", `{"model":"gpt-5.5","input":"hello","tools":[{"type":"custom","name":"apply_patch"}]}`, `{"id":"resp_test","object":"response","status":"completed","output":[],"usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}`, (*proxy.Service).ProxyOpenAIResponses},
		{"gemini", providers.ProviderGoogle, "gemini-2.5-pro", "/v1beta/models/gemini-2.5-pro:generateContent", `{"model":"gemini-2.5-pro","contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, `{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":4,"totalTokenCount":16}}`, (*proxy.Service).ProxyGeminiGenerateContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
					workers := testObservationWorkers(t)
					release := make(chan struct{})
					t.Cleanup(func() { close(release) })
					started := make(chan struct{}, 3)
					block := func(ctx context.Context, _ []byte) error {
						started <- struct{}{}
						select {
						case <-release:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					log := observability.FromContext(context.Background())
					workers.Database.Submit(observability.WorkAttempt, nil, time.Minute, log, block)
					for range 2 {
						workers.Remote.Submit(observability.WorkOutcome, nil, time.Minute, log, block)
					}
					for range 3 {
						<-started
					}
					requestBody, responseBody, contentType := tc.request, tc.response, "application/json"
					if stream {
						requestBody = strings.TrimSuffix(requestBody, "}") + `,"stream":true}`
						responseBody = "data: " + responseBody + "\n\ndata: [DONE]\n\n"
						if tc.name == "anthropic" {
							responseBody = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":12}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
						}
						if tc.name == "chat" {
							responseBody = "data: " + strings.ReplaceAll(strings.ReplaceAll(tc.response, `"message":`, `"delta":`), `"chat.completion"`, `"chat.completion.chunk"`) + "\n\ndata: [DONE]\n\n"
						}

						if tc.name == "responses" {
							responseBody = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + tc.response + "}\n\n"
						}
						contentType = "text/event-stream"
					}
					run := func(held bool) *httptest.ResponseRecorder {
						provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
							w.Header().Set("Content-Type", contentType)
							_, _ = w.Write([]byte(responseBody))
							if f, ok := w.(http.Flusher); ok {
								f.Flush()
							}
						}}
						service := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: tc.provider, Model: tc.model}}, map[string]providers.Client{tc.provider: provider}, nil, false, nil, nil, false, tc.provider, tc.model, newCaptureTelemetry())
						if tc.name == "chat" {
							service.WithOpenAIResponsesBroad(false)
						}
						if held {
							service.WithObservationWorkers(workers)
						}
						rec := httptest.NewRecorder()
						done := make(chan error, 1)
						go func() {
							req := httptest.NewRequest(http.MethodPost, tc.path, nil)
							req.Header.Set(routingMarkerHeader, "off")
							done <- tc.call(service, authedCtx("11111111-1111-1111-1111-111111111111"), []byte(requestBody), rec, req)
						}()
						select {
						case err := <-done:
							require.NoError(t, err)
						case <-time.After(time.Second):
							t.Fatal("held observations delayed response completion")
						}
						require.Len(t, provider.proxyBodies, 1)
						return rec
					}
					baseline, held := run(false), run(true)
					assert.NotEmpty(t, baseline.Body.String())
					assert.Equal(t, baseline.Body.String(), held.Body.String())
					assert.Equal(t, baseline.Header().Get("Content-Type"), held.Header().Get("Content-Type"))
				})
			}
		})
	}
}
