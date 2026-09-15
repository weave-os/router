package proxy_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	capAnthropicModel = "claude-sonnet-5"
	capChatModel      = "deepseek/deepseek-v4-flash"
	capResponsesModel = "gpt-5.6-luna"
	capGeminiModel    = "gemini-3-flash-preview"
)

func TestProxyOutputLimitAcrossResponsePaths(t *testing.T) {
	paths := []struct {
		ingress  string
		wire     string
		provider string
		model    string
	}{
		{"messages", "anthropic", providers.ProviderAnthropic, capAnthropicModel},
		{"messages", "chat", providers.ProviderOpenRouter, capChatModel},
		{"messages", "responses", providers.ProviderOpenAI, capResponsesModel},
		{"messages", "gemini", providers.ProviderGoogle, capGeminiModel},
		{"chat", "anthropic", providers.ProviderAnthropic, capAnthropicModel},
		{"chat", "chat", providers.ProviderOpenRouter, capChatModel},
		{"chat", "responses", providers.ProviderOpenAI, capResponsesModel},
		{"chat", "gemini", providers.ProviderGoogle, capGeminiModel},
		{"responses", "responses", providers.ProviderOpenAI, capResponsesModel},
		{"gemini", "gemini", providers.ProviderGoogle, capGeminiModel},
	}
	for _, path := range paths {
		for _, stream := range []bool{false, true} {
			for _, capped := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t/capped=%t", path.ingress, path.wire, stream, capped), func(t *testing.T) {
					store := newFakePinStore()
					response := capWireResponse(path.wire, stream || (path.wire == "responses" && path.ingress != "responses"), capped)
					provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
						contentType := "application/json"
						if strings.HasPrefix(response, "data:") || strings.HasPrefix(response, "event:") {
							contentType = "text/event-stream"
						}
						w.Header().Set("Content-Type", contentType)
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(response))
					}}
					svc := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: path.provider, Model: path.model, Reason: "fresh"}},
						map[string]providers.Client{path.provider: provider}, nil, false, nil, store, false, path.provider, path.model, nil).
						WithOpenAIResponsesBroad(path.wire == "responses").
						WithNativeAnthropicResponseSignals(false).
						WithNativeOpenAIResponseSignals(false)
					ctx := authedCtx(uuid.NewString())
					rec := httptest.NewRecorder()
					body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64000,"stream":%t,"messages":[{"role":"user","content":"Inspect the repository files"}]}`, path.model, stream))
					switch path.ingress {
					case "messages":
						require.NoError(t, svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
					case "chat":
						require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))
					case "responses":
						body = []byte(fmt.Sprintf(`{"model":"auto","stream":%t,"input":"Inspect the repository files"}`, stream))
						require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))
					case "gemini":
						body = []byte(`{"contents":[{"role":"user","parts":[{"text":"Inspect the repository files"}]}],"generationConfig":{"maxOutputTokens":64000}}`)
						action := "generateContent"
						if stream {
							action = "streamGenerateContent"
						}
						require.NoError(t, svc.ProxyGeminiGenerateContent(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1beta/models/"+path.model+":"+action, nil)))
					}
					assert.Equal(t, http.StatusOK, rec.Code)
					assert.NotEmpty(t, rec.Body.String())
					require.NotEmpty(t, store.usages, "a pin store requires usage capture even with all telemetry off")
					got := store.usages[len(store.usages)-1]
					assert.Equal(t, path.model, got.ServedModel)
					assert.Equal(t, 32000, got.OutputTokens)
					assert.Equal(t, capped, got.OutputLimitReached)
				})
			}
		}
	}
}

func capWireResponse(wire string, stream, capped bool) string {
	switch wire {
	case "anthropic":
		reason := "end_turn"
		if capped {
			reason = "max_tokens"
		}
		if !stream {
			return fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"answer"}],"stop_reason":%q,"usage":{"input_tokens":100,"output_tokens":32000}}`, reason)
		}
		return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":100}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			fmt.Sprintf("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":32000}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", reason)
	case "chat":
		reason := "stop"
		if capped {
			reason = "length"
		}
		if !stream {
			return fmt.Sprintf(`{"id":"chat_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":%q}],"usage":{"prompt_tokens":100,"completion_tokens":32000}}`, reason)
		}
		return "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":null}]}\n\n" +
			fmt.Sprintf("data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":32000}}\n\ndata: [DONE]\n\n", reason)
	case "gemini":
		reason := "STOP"
		if capped {
			reason = "MAX_TOKENS"
		}
		body := fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"text":"answer"}]},"finishReason":%q}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":32000,"totalTokenCount":32100}}`, reason)
		if stream {
			return "data: " + body + "\n\n"
		}
		return body
	case "responses":
		status, details := "completed", "null"
		if capped {
			status, details = "incomplete", `{"reason":"max_output_tokens"}`
		}
		body := fmt.Sprintf(`{"id":"resp_1","object":"response","status":%q,"incomplete_details":%s,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":100,"output_tokens":32000}}`, status, details)
		if !stream {
			return body
		}
		return "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n" +
			"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"answer\"}\n\n" +
			fmt.Sprintf("data: {\"type\":\"response.%s\",\"response\":%s}\n\n", status, body)
	}
	panic("unknown test wire format")
}

func TestTurnLoop_HighOutputKeepsActiveAndExpiredPins(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%t", expired), func(t *testing.T) {
			const freshModel = "claude-haiku-4-5"
			store := newFakePinStore()
			store.hasPin = true
			until := time.Now().Add(time.Hour)
			if expired {
				until = time.Now().Add(-time.Minute)
			}
			store.pin = sessionpin.Pin{Provider: providers.ProviderAnthropic, Model: capAnthropicModel, LastServedModel: capAnthropicModel,
				Reason: "cluster", LastOutputTokens: 32000, LastTurnEndedAt: time.Now().Add(-time.Second), PinnedUntil: until}
			routes := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: freshModel, Reason: "fresh"}}
			svc := newPinSvc(routes, store).WithPlannerEnabled(false).WithAvailableModels(map[string]struct{}{capAnthropicModel: {}, freshModel: {}})
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyMessages(authedCtx(uuid.NewString()), []byte(pinTestBody), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))
			assert.Equal(t, capAnthropicModel, rec.Header().Get(proxy.HeaderRouterModel))
		})
	}
}
