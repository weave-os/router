package translate_test

import (
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const chatSplitReasoningThenText = "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"Need \"},\"finish_reason\":null}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"both.\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done.\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

func streamedChatReasoning(t *testing.T, body string) []gjson.Result {
	t.Helper()
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "client-model")
	require.NoError(t, w.Prelude(true))
	reasoningToResponsesDrive(t, w, "text/event-stream", body)
	return reasoningToResponsesDataFrames(rec.Body.String())
}

func TestResponsesWriter_ChatReasoningStreamsAsSummaryBeforeMessage(t *testing.T) {
	var types []string
	var completed gjson.Result
	for _, frame := range streamedChatReasoning(t, chatSplitReasoningThenText) {
		if frame.Get("item_id").Exists() || frame.Get("item.type").Str == "reasoning" {
			types = append(types, frame.Get("type").Str)
		}
		if frame.Get("type").Str == "response.completed" {
			completed = frame.Get("response.output")
		}
	}
	assert.Equal(t, []string{
		"response.output_item.added",
		"response.reasoning_summary_part.added",
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done",
		"response.reasoning_summary_part.done",
		"response.output_item.done",
	}, types[:6])
	assert.Equal(t, "reasoning", completed.Get("0.type").Str)
	assert.Equal(t, "Need both.", completed.Get("0.summary.0.text").Str)
	assert.False(t, completed.Get("0.encrypted_content").Exists())
	assert.Equal(t, "message", completed.Get("1.type").Str)
}

func TestResponsesWriter_ChatReasoningPrecedesFunctionCalls(t *testing.T) {
	var reasoningIndex, firstCallIndex int64 = -1, -1
	for _, frame := range streamedChatReasoning(t, reasoningToResponsesUpstream) {
		if frame.Get("type").Str != "response.output_item.added" {
			continue
		}
		switch frame.Get("item.type").Str {
		case "reasoning":
			reasoningIndex = frame.Get("output_index").Int()
		case "function_call":
			if firstCallIndex < 0 {
				firstCallIndex = frame.Get("output_index").Int()
			}
		}
	}
	require.GreaterOrEqual(t, reasoningIndex, int64(0))
	assert.Less(t, reasoningIndex, firstCallIndex)
}

func TestResponsesWriter_NonStreamingChatReasoningLeadsOutput(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "client-model")
	require.NoError(t, w.Prelude(false))
	reasoningToResponsesDrive(t, w, "application/json", `{"choices":[{"message":{"role":"assistant","reasoning_content":"Think.","content":"Answer."},"finish_reason":"stop"}]}`)
	output := gjson.Get(rec.Body.String(), "output")
	assert.Equal(t, "reasoning", output.Get("0.type").Str)
	assert.Equal(t, "Think.", output.Get("0.summary.0.text").Str)
	assert.Equal(t, "message", output.Get("1.type").Str)
}

func TestChatReasoningEchoConvertsWithoutPinningTheTurn(t *testing.T) {
	var reasoningItem string
	for _, frame := range streamedChatReasoning(t, reasoningToResponsesUpstream) {
		if frame.Get("type").Str == "response.completed" {
			reasoningItem = frame.Get(`response.output.#(type=="reasoning")`).Raw
		}
	}
	require.NotEmpty(t, reasoningItem)
	body := []byte(`{"model":"gpt-5.6-sol","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Read both."}]},` + reasoningItem + `,` +
		`{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_a","output":"a"}]}`)
	for _, portable := range []bool{false, true} {
		converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: portable})
		require.NoError(t, err)
		assert.False(t, converted.Requirements.NativeOnly, "portable=%v", portable)
		assert.False(t, converted.Requirements.ReasoningReplay, "portable=%v", portable)
		assert.NotContains(t, string(converted.Body), "Need both files.")
	}
	stripped := translate.StripRouterReasoningFromResponsesInput(body)
	assert.Equal(t, []string{"message", "function_call", "function_call_output"}, inputTypes(stripped))
}

func TestStripRouterReasoningKeepsUpstreamReasoning(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_abc","summary":[]},{"type":"reasoning","id":"rs_router_x","summary":[],"encrypted_content":"enc"}]}`)
	stripped := translate.StripRouterReasoningFromResponsesInput(body)
	assert.JSONEq(t, string(body), string(stripped))
}

func inputTypes(body []byte) []string {
	var out []string
	for _, item := range gjson.GetBytes(body, "input").Array() {
		out = append(out, item.Get("type").Str)
	}
	return out
}
