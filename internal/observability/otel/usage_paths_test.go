package otel_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/usage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type usageTranslator interface {
	http.ResponseWriter
	Finalize() error
}

func TestUsageFactsNativeAndTranslatedPaths(t *testing.T) {
	const anthropicUsage = `{"input_tokens":20,"output_tokens":10,"cache_creation_input_tokens":30,"cache_read_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":10},"speed":"fast","inference_geo":"us"}`
	const chatUsage = `{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":50,"cache_write_tokens":30},"completion_tokens_details":{"reasoning_tokens":3}}`
	const responsesUsage = `{"input_tokens":100,"output_tokens":10,"total_tokens":110,"input_tokens_details":{"cached_tokens":50,"cache_write_tokens":30},"output_tokens_details":{"reasoning_tokens":3}}`
	const geminiUsage = `{"promptTokenCount":100,"candidatesTokenCount":7,"thoughtsTokenCount":3,"totalTokenCount":110,"cachedContentTokenCount":80}`
	type path struct {
		name, provider, body, sseBody string
		translator                    func(http.ResponseWriter, translate.UsageSink) usageTranslator
		prelude                       bool
	}
	cases := []path{
		{"anthropic-chat", providers.ProviderAnthropic, `{"id":"msg_synthetic","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":` + anthropicUsage + `}`,
			"event: message_start\ndata: {\"message\":{\"id\":\"msg_synthetic\",\"usage\":" + anthropicUsage + "}}\n\nevent: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":10}}\n\nevent: message_stop\ndata: {}\n\n",
			func(w http.ResponseWriter, s translate.UsageSink) usageTranslator {
				return translate.NewSSETranslator(w, "synthetic-model", s)
			}, false},
		{"chat-anthropic", providers.ProviderOpenAI, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":` + chatUsage + `}`,
			"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":" + chatUsage + "}\n\ndata: [DONE]\n\n",
			func(w http.ResponseWriter, s translate.UsageSink) usageTranslator {
				return translate.NewAnthropicSSETranslator(w, "synthetic-model", s)
			}, true},
		{"xai-anthropic", providers.ProviderXAI, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":7,"completion_tokens_details":{"reasoning_tokens":3}}}`,
			"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":7,\"completion_tokens_details\":{\"reasoning_tokens\":3}}}\n\ndata: [DONE]\n\n",
			func(w http.ResponseWriter, s translate.UsageSink) usageTranslator {
				tr := translate.NewAnthropicSSETranslator(w, "synthetic-model", s)
				tr.SetUsageOutputBasis(usage.OutputExcludesReasoning)
				return tr
			}, true},
		{"responses-chat", providers.ProviderOpenAI, `{"id":"resp_synthetic","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":` + responsesUsage + `}`,
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_synthetic\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":" + responsesUsage + "}}\n\n",
			func(w http.ResponseWriter, s translate.UsageSink) usageTranslator {
				return translate.NewResponsesToOpenAIChatWriter(w, "synthetic-model", s)
			}, true},
		{"responses-anthropic", providers.ProviderOpenAI, `{"id":"resp_synthetic","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":` + responsesUsage + `}`,
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_synthetic\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":" + responsesUsage + "}}\n\n",
			func(w http.ResponseWriter, s translate.UsageSink) usageTranslator {
				return translate.NewResponsesToAnthropicWriter(w, "synthetic-model", s)
			}, true},
		{"gemini-chat", providers.ProviderGoogle, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":` + geminiUsage + `}`,
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":" + geminiUsage + "}\n\n",
			func(w http.ResponseWriter, s translate.UsageSink) usageTranslator {
				return translate.NewGeminiToOpenAISSETranslator(w, "synthetic-model", s)
			}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			native := otel.NewUsageExtractor(httptest.NewRecorder(), tt.provider)
			_, err := native.Write([]byte(tt.body))
			require.NoError(t, err)
			assert.True(t, native.UsageFacts().Present)
			for _, stream := range []bool{false, true} {
				label := "buffered"
				if stream {
					label = "streaming"
				}
				t.Run(label, func(t *testing.T) {
					nativeStream := otel.NewUsageExtractor(httptest.NewRecorder(), tt.provider)
					_, err := nativeStream.Write([]byte(tt.sseBody))
					require.NoError(t, err)
					assert.Equal(t, native.UsageFacts(), nativeStream.UsageFacts())
					sink := otel.NewUsageExtractor(nil, tt.provider)
					rec := httptest.NewRecorder()
					w := tt.translator(rec, sink)
					if p, ok := w.(interface{ Prelude(bool) error }); tt.prelude && ok {
						require.NoError(t, p.Prelude(stream))
					}
					payload := tt.body
					// Responses adapters intentionally consume upstream SSE for both client modes.
					if stream || tt.name == "responses-chat" || tt.name == "responses-anthropic" {
						payload = tt.sseBody
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
					} else {
						w.Header().Set("Content-Type", "application/json")
					}
					w.WriteHeader(http.StatusOK)
					for i := 0; i < len(payload); i += 7 {
						_, err = w.Write([]byte(payload[i:min(i+7, len(payload))]))
						require.NoError(t, err)
					}
					require.NoError(t, w.Finalize())
					assert.Equal(t, native.UsageFacts(), sink.UsageFacts())
					input, output := sink.Tokens()
					assert.Positive(t, input)
					assert.Equal(t, 10, output)
				})
			}
		})
	}
}
