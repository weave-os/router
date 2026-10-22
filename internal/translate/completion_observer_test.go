package translate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const observerTestFooter = "\n\n_Weave Router feedback:_ `/rf +` good · `/rf -` poor"

func writeCompletionFragments(t *testing.T, writer http.ResponseWriter, payload string) {
	t.Helper()
	for offset := 0; offset < len(payload); offset += 3 {
		fragment := []byte(payload[offset:min(offset+3, len(payload))])
		written, err := writer.Write(fragment)
		require.NoError(t, err)
		require.Equal(t, len(fragment), written)
	}
}

func TestCompletionObserverPreservesSourceEligibilityAcrossConversion(t *testing.T) {
	tests := []struct {
		name     string
		format   Format
		payload  string
		eligible bool
	}{
		{
			name: "Chat natural answer", format: FormatOpenAI, eligible: true,
			payload: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		},
		{
			name: "Chat content filter remains suppressed after conversion", format: FormatOpenAI,
			payload: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n",
		},
		{
			name: "Chat absent finish reason cannot become a natural answer", format: FormatOpenAI,
			payload: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n\ndata: [DONE]\n\n",
		},
		{
			name: "Gemini natural answer", format: FormatGemini, eligible: true,
			payload: "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"answer\"}]},\"finishReason\":\"STOP\"}]}\n\n",
		},
		{
			name: "Gemini safety remains suppressed after conversion", format: FormatGemini,
			payload: "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"answer\"}]},\"finishReason\":\"SAFETY\"}]}\n\n",
		},
		{
			name: "Gemini unknown finish remains suppressed after conversion", format: FormatGemini,
			payload: "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"answer\"}]},\"finishReason\":\"FUTURE_REASON\"}]}\n\n",
		},
		{
			name: "Responses natural answer", format: FormatOpenAIResponses, eligible: true,
			payload: "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}]}}\n\n",
		},
		{
			name: "Responses explicit completed event without repeated status", format: FormatOpenAIResponses, eligible: true,
			payload: "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}]}}\n\n",
		},
		{
			name: "Responses refusal remains suppressed after conversion", format: FormatOpenAIResponses,
			payload: "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"},{\"type\":\"refusal\",\"refusal\":\"declined\"}]}]}}\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
			var converter http.ResponseWriter
			var finalize func() error
			if tt.format == FormatOpenAI {
				translator := NewAnthropicSSETranslator(NewAnthropicRoutingFooterWriter(recorder, delivery), "synthetic-model", nil)
				converter, finalize = translator, translator.Finalize
			} else {
				adapter := NewOpenAIRoutingFooterWriter(recorder, delivery)
				if tt.format == FormatGemini {
					translator := NewGeminiToOpenAISSETranslator(adapter, "synthetic-model", nil)
					converter, finalize = translator, translator.Finalize
				} else {
					translator := NewResponsesToOpenAIChatWriter(adapter, "synthetic-model", nil)
					require.NoError(t, translator.Prelude(true))
					converter, finalize = translator, translator.Finalize
				}
			}
			observer := NewCompletionObserver(converter, tt.format, true, delivery)
			observer.Header().Set("Content-Type", "text/event-stream")
			observer.WriteHeader(http.StatusOK)
			writeCompletionFragments(t, observer, tt.payload)
			require.NoError(t, observer.Finish(nil))
			finalErr := finalize()
			if tt.eligible {
				require.NoError(t, finalErr)
			}
			assert.Contains(t, recorder.Body.String(), "answer")
			assert.Equal(t, tt.eligible, delivery.facts.Eligible())
			assert.Equal(t, tt.eligible, strings.Contains(recorder.Body.String(), "Weave Router feedback"))
		})
	}
}

func TestCompletionObserverSelectsExplicitPrimaryIndex(t *testing.T) {
	for name, format := range map[string]Format{"Chat": FormatOpenAI, "Gemini": FormatGemini} {
		t.Run(name, func(t *testing.T) {
			for _, primaryNatural := range []bool{false, true} {
				delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
				observer := NewCompletionObserver(httptest.NewRecorder(), format, true, delivery)
				payload := `{"choices":[{"index":1,"delta":{"content":"secondary"},"finish_reason":"stop"},{"index":0,"delta":{"content":"primary"},"finish_reason":"length"}]}`
				if format == FormatGemini {
					payload = `{"candidates":[{"index":1,"content":{"parts":[{"text":"secondary"}]},"finishReason":"STOP"},{"index":0,"content":{"parts":[{"text":"primary"}]},"finishReason":"MAX_TOKENS"}]}`
				}
				if primaryNatural {
					payload = strings.ReplaceAll(payload, `"length"`, `"stop"`)
					payload = strings.ReplaceAll(payload, `"MAX_TOKENS"`, `"STOP"`)
				}
				writeCompletionFragments(t, observer, "data: "+payload+"\n\n")
				require.NoError(t, observer.Finish(nil))
				assert.Equal(t, primaryNatural, delivery.facts.Eligible())
			}
		})
	}
}

func TestCompletionObserverIgnoresEmptyLegacyFunctionCall(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, call := range []string{`null`, `{}`, `{"name":""}`} {
			delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
			observer := NewCompletionObserver(httptest.NewRecorder(), FormatOpenAI, streaming, delivery)
			messageField := "message"
			if streaming {
				messageField = "delta"
			}
			payload := `{"choices":[{"index":0,"` + messageField + `":{"content":"answer","function_call":` + call + `},"finish_reason":"stop"}]}`
			if streaming {
				payload = "data: " + payload + "\n\n"
			}
			writeCompletionFragments(t, observer, payload)
			require.NoError(t, observer.Finish(nil))
			assert.Equal(t, observerTestFooter, delivery.Take(), "streaming=%t function_call=%s", streaming, call)
		}
	}
}

func TestCompletionObserverSuppressesLegacyFunctionCallFragments(t *testing.T) {
	for _, call := range []string{`{"name":"tool"}`, `{"arguments":"{}"}`} {
		delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
		observer := NewCompletionObserver(httptest.NewRecorder(), FormatOpenAI, true, delivery)
		payload := `data: {"choices":[{"index":0,"delta":{"content":"answer","function_call":` + call + `},"finish_reason":"stop"}]}` + "\n\n"
		writeCompletionFragments(t, observer, payload)
		require.NoError(t, observer.Finish(nil))
		assert.Empty(t, delivery.Take())
	}
}

func TestCompletionObserverFragmentsPreserveProviderBytes(t *testing.T) {
	recorder := httptest.NewRecorder()
	delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
	observer := NewCompletionObserver(recorder, FormatAnthropic, true, delivery)
	payload := ": heartbeat\r\n\r\nevent: content_block_start\r\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"answer ☀\"}}\r\n\r\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}"
	writeCompletionFragments(t, observer, payload)
	assert.False(t, delivery.facts.Closed, "unterminated final record waits for EOF")
	assert.Equal(t, payload, recorder.Body.String(), "partial provider records pass through before finalization")
	require.NoError(t, observer.Finish(nil))
	assert.Equal(t, payload, recorder.Body.String())
	assert.Equal(t, observerTestFooter, delivery.Take())
	assert.Empty(t, delivery.Take(), "one provider completion authorizes one suffix")
}

func TestCompletionObserverForwardsPartialProviderRecordsImmediately(t *testing.T) {
	recorder := httptest.NewRecorder()
	delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
	observer := NewCompletionObserver(recorder, FormatOpenAI, true, delivery)
	var forwarded strings.Builder
	for _, fragment := range []string{"data: {", `"choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}`, "\n\n"} {
		_, err := observer.Write([]byte(fragment))
		require.NoError(t, err)
		forwarded.WriteString(fragment)
		assert.Equal(t, forwarded.String(), recorder.Body.String())
	}
	assert.Equal(t, observerTestFooter, delivery.Take())
}

func TestCompletionObserverRejectsSecondaryOnlyCompletion(t *testing.T) {
	for format, payload := range map[Format]string{
		FormatOpenAI: `{"choices":[{"index":1,"delta":{"content":"secondary"},"finish_reason":"stop"}]}`,
		FormatGemini: `{"candidates":[{"index":1,"content":{"parts":[{"text":"secondary"}]},"finishReason":"STOP"}]}`,
	} {
		delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
		observer := NewCompletionObserver(httptest.NewRecorder(), format, true, delivery)
		writeCompletionFragments(t, observer, "data: "+payload+"\n\n")
		require.NoError(t, observer.Finish(nil))
		assert.Empty(t, delivery.Take())
	}
}

func TestCompletionObserverRecognizesEchoAcrossTextDeltas(t *testing.T) {
	delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
	observer := NewCompletionObserver(httptest.NewRecorder(), FormatOpenAI, true, delivery)
	for _, letter := range "answer" + observerTestFooter {
		encoded, err := json.Marshal(string(letter))
		require.NoError(t, err)
		writeCompletionFragments(t, observer, `data: {"choices":[{"index":0,"delta":{"content":`+string(encoded)+`}}]}`+"\n\n")
	}
	writeCompletionFragments(t, observer, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	require.NoError(t, observer.Finish(nil))
	assert.True(t, delivery.facts.Echoed)
	assert.Empty(t, delivery.Take(), "an echoed footer must not be appended again")
}

func TestCompletionObserverAnthropicUsageDeltaPreservesNaturalStop(t *testing.T) {
	delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
	observer := NewCompletionObserver(httptest.NewRecorder(), FormatAnthropic, true, delivery)
	payload := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"answer\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":null},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	writeCompletionFragments(t, observer, payload)
	require.NoError(t, observer.Finish(nil))
	assert.Equal(t, observerTestFooter, delivery.Take())
}

func TestFeedbackDeliveryAttemptResetAndRequestLatch(t *testing.T) {
	delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
	failed := NewCompletionObserver(httptest.NewRecorder(), FormatOpenAI, true, delivery)
	writeCompletionFragments(t, failed, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\",\"tool_calls\":[{\"id\":\"call_test\"}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
	abandoned := errors.New("synthetic transport fault")
	require.ErrorIs(t, failed.Finish(abandoned), abandoned)
	assert.Empty(t, delivery.Take())

	for attempt := 0; attempt < 2; attempt++ {
		observer := NewCompletionObserver(httptest.NewRecorder(), FormatOpenAI, true, delivery)
		assert.False(t, delivery.facts.Tool, "abandoned attempt tool evidence must not leak")
		assert.False(t, delivery.facts.Failed, "abandoned transport failure must not leak")
		assert.Empty(t, delivery.Take(), "new attempt cannot reuse prior completion evidence")
		writeCompletionFragments(t, observer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
		require.NoError(t, observer.Finish(nil))
		if attempt == 0 {
			assert.Equal(t, observerTestFooter, delivery.Take())
		} else {
			assert.Empty(t, delivery.Take(), "new attempt must not reset request delivery latch")
		}
	}
}

func TestFeedbackDeliveryCancellationSuppressesCompletedAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	delivery := NewFeedbackDelivery(ctx, FeedbackIntent{Text: observerTestFooter})
	observer := NewCompletionObserver(httptest.NewRecorder(), FormatOpenAI, true, delivery)
	writeCompletionFragments(t, observer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
	require.NoError(t, observer.Finish(nil))
	assert.True(t, delivery.facts.Eligible())
	cancel()
	assert.Empty(t, delivery.Take())
}

type completionProgressRecorder struct {
	*httptest.ResponseRecorder
	output    func()
	reasoning func()
}

func (r *completionProgressRecorder) ArmOutputProgress(mark func()) bool {
	r.output = mark
	return true
}

func (r *completionProgressRecorder) ArmReasoningProgress(mark func()) bool {
	r.reasoning = mark
	return true
}

func TestCompletionObserverForwardsBothProgressHooksWithoutReportingProgress(t *testing.T) {
	recorder := &completionProgressRecorder{ResponseRecorder: httptest.NewRecorder()}
	delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
	observer := NewCompletionObserver(recorder, FormatOpenAI, true, delivery)
	outputCalls, reasoningCalls := 0, 0
	require.True(t, observer.ArmOutputProgress(func() { outputCalls++ }))
	require.True(t, observer.ArmReasoningProgress(func() { reasoningCalls++ }))
	writeCompletionFragments(t, observer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
	require.NoError(t, observer.Finish(nil))
	assert.Equal(t, observerTestFooter, delivery.Take())
	assert.Zero(t, outputCalls)
	assert.Zero(t, reasoningCalls)
	require.NotNil(t, recorder.output)
	require.NotNil(t, recorder.reasoning)
	recorder.output()
	recorder.reasoning()
	assert.Equal(t, 1, outputCalls)
	assert.Equal(t, 1, reasoningCalls)
}

func TestCompletionObserverBufferedNativeResponsesEligibility(t *testing.T) {
	for _, tt := range []struct {
		name     string
		response string
		eligible bool
	}{
		{name: "natural", eligible: true, response: `{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`},
		{name: "natural answer after hosted search", eligible: true, response: `{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"web_search_call","id":"ws_test","status":"completed","action":{"type":"search","query":"synthetic"}}]}`},
		{name: "truncated", response: `{"id":"resp_test","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`},
		{name: "refusal", response: `{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"},{"type":"refusal","refusal":"declined"}]}]}`},
		{name: "native tool", response: `{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"custom_tool_call","id":"ctc_test","call_id":"call_test","name":"exec","input":"synthetic"}]}`},
		{name: "unknown output", response: `{"id":"resp_test","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"future_item","opaque":{"keep":true}}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
			adapter := NewResponsesFeedbackWriter(recorder, delivery)
			observer := NewCompletionObserver(adapter, FormatOpenAIResponses, false, delivery)
			observer.Header().Set("Content-Type", "application/json")
			observer.WriteHeader(http.StatusOK)
			writeCompletionFragments(t, observer, tt.response)
			require.NoError(t, observer.Finish(nil))
			require.NoError(t, adapter.Finalize())
			assert.Equal(t, tt.eligible, delivery.facts.Eligible())
			expectedText := "answer"
			if tt.eligible {
				expectedText += observerTestFooter
			}
			assert.Equal(t, expectedText, gjson.GetBytes(recorder.Body.Bytes(), "output.0.content.0.text").Str)
			if !tt.eligible {
				assert.JSONEq(t, tt.response, recorder.Body.String(), "suppression preserves native extension payloads")
			}
		})
	}
}

func TestCompletionObserverHostedToolCanFinishNaturalAnswer(t *testing.T) {
	delivery := NewFeedbackDelivery(context.Background(), FeedbackIntent{Text: observerTestFooter})
	observer := NewCompletionObserver(httptest.NewRecorder(), FormatAnthropic, false, delivery)
	_, err := observer.Write([]byte(`{"content":[{"type":"server_tool_use","id":"search_test","name":"web_search"},{"type":"text","text":"answer"}],"stop_reason":"end_turn"}`))
	require.NoError(t, err)
	require.NoError(t, observer.Finish(nil))
	assert.Equal(t, observerTestFooter, delivery.Take())
}
