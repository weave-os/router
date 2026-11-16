package translate_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/translate"
)

const responsesInvariantBadge = codexResponsesBadgeSentinelForTest + "✦ **Weave Router** → gpt-5.6-terra · best pick for this turn\n\n"

func marshalResponsesInput(t *testing.T, input []map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "gpt-5.6-sol", "input": input})
	require.NoError(t, err)
	return body
}

func TestResponsesToChatCompletions_TypelessRoleItemIsAMessage(t *testing.T) {
	out, _, _, err := translate.ResponsesToChatCompletions([]byte(`{"model":"gpt-5.6-sol","input":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)

	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 1)
	assert.Equal(t, "user", messages[0].Get("role").Str)
	assert.Equal(t, "hello", messages[0].Get("content").Str)
}

func TestResponsesToChatCompletions_StripsBadgeOnlyFromFirstAssistantTextPart(t *testing.T) {
	body := marshalResponsesInput(t, []map[string]any{
		{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": responsesInvariantBadge + "question"}}},
		{"type": "message", "role": "assistant", "content": []map[string]any{
			{"type": "output_text", "text": responsesInvariantBadge + "first"},
			{"type": "output_text", "text": responsesInvariantBadge + "second"},
		}},
	})

	out, _, _, err := translate.ResponsesToChatCompletions(body)
	require.NoError(t, err)

	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 2)
	assert.Contains(t, messages[0].Get("content").Raw, "Weave Router", "user-authored text is never stripped")
	assistant := messages[1].Get("content").String()
	assert.True(t, strings.HasPrefix(assistant, "first"), "first assistant part loses its badge: %q", assistant)
	assert.Contains(t, assistant, responsesInvariantBadge+"second", "later assistant parts keep their text verbatim")
}

func TestStripRoutingBadgeFromResponsesInput_NonTextPartKeepsBadgeOnlyItem(t *testing.T) {
	body := marshalResponsesInput(t, []map[string]any{
		{"type": "message", "role": "assistant", "content": []map[string]any{
			{"type": "output_text", "text": responsesInvariantBadge},
			{"type": "refusal", "refusal": "no"},
		}},
	})

	out, err := translate.StripRoutingBadgeFromResponsesInput(body)
	require.NoError(t, err)

	input := gjson.GetBytes(out, "input").Array()
	require.Len(t, input, 1)
	assert.Empty(t, input[0].Get("content.0.text").Str)
	assert.Equal(t, "no", input[0].Get("content.1.refusal").Str)
}

func TestStripRoutingBadgeFromResponsesInput_BadgeOnlyItemIsDropped(t *testing.T) {
	body := marshalResponsesInput(t, []map[string]any{
		{"type": "message", "role": "user", "content": "go"},
		{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": responsesInvariantBadge}}},
	})

	out, err := translate.StripRoutingBadgeFromResponsesInput(body)
	require.NoError(t, err)

	input := gjson.GetBytes(out, "input").Array()
	require.Len(t, input, 1)
	assert.Equal(t, "user", input[0].Get("role").Str)
}

func TestStripRoutingBadgeFromResponsesInput_StripsOnlyFirstTextPart(t *testing.T) {
	body := marshalResponsesInput(t, []map[string]any{
		{"type": "message", "role": "assistant", "content": []map[string]any{
			{"type": "output_text", "text": responsesInvariantBadge + "first"},
			{"type": "output_text", "text": responsesInvariantBadge + "second"},
		}},
	})

	out, err := translate.StripRoutingBadgeFromResponsesInput(body)
	require.NoError(t, err)

	assert.Equal(t, "first", gjson.GetBytes(out, "input.0.content.0.text").Str)
	assert.Equal(t, responsesInvariantBadge+"second", gjson.GetBytes(out, "input.0.content.1.text").Str)
}

func TestResponsesWriter_ForgetWithheldHeadersAllowsRescueOnAnotherFormat(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.5")
	w.SetPassthrough()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	w.ForgetWithheldHeaders()

	assert.True(t, w.ClearPassthrough())
}

func TestResponsesWriter_WithheldHeadersWithoutForgetBlockRescue(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.5")
	w.SetPassthrough()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	assert.False(t, w.ClearPassthrough())
}

func TestResponsesWriter_PassthroughDropsUpstreamFramingHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.5")
	w.SetPassthrough()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Content-Length", "123")
	w.Header().Set("Content-Encoding", "gzip")
	w.WriteHeader(http.StatusOK)

	assert.Empty(t, rec.Header().Get("Content-Length"))
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
}

func TestResponsesWriter_PassthroughStreamsNativeSSEWithoutContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.5")
	w.SetPassthrough()
	w.WriteHeader(http.StatusOK)

	first := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"
	_, err := w.Write([]byte(first))
	require.NoError(t, err)

	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, first, rec.Body.String(), "the first native event streams before Finalize, exactly once")
}

func TestResponsesWriter_PassthroughBadgeSkipsEmptyFirstDelta(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.5")
	w.SetPassthroughBadge()
	w.SetBadgeText(passthroughTestMarker)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	native := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"ok\"}\n\n"
	_, err := w.Write([]byte(native))
	require.NoError(t, err)

	var deltas []string
	for _, event := range parseSSEEvents(t, rec.Body.Bytes()) {
		if event["type"] == "response.output_text.delta" {
			deltas = append(deltas, mustAs[string](t, event["delta"]))
		}
	}
	require.Len(t, deltas, 2)
	assert.Empty(t, deltas[0])
	assert.Contains(t, deltas[1], passthroughTestMarker)
	assert.True(t, strings.HasSuffix(deltas[1], "ok"))
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexDropsBadgeOnlyAssistantString(t *testing.T) {
	body := marshalResponsesInput(t, []map[string]any{
		{"type": "message", "role": "user", "content": "go"},
		{"type": "message", "role": "assistant", "content": responsesInvariantBadge},
		{"type": "message", "role": "user", "content": "again"},
	})

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)

	messages := gjson.GetBytes(converted.Body, "messages").Array()
	require.Len(t, messages, 2)
	assert.Equal(t, "user", messages[0].Get("role").Str)
	assert.Equal(t, "user", messages[1].Get("role").Str)
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexAssistantImageStaysNative(t *testing.T) {
	body := marshalResponsesInput(t, []map[string]any{
		{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "input_image", "image_url": "data:image/png;base64,iVBORw0KGgo="}}},
	})

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)

	assert.True(t, converted.Requirements.NativeOnly)
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexPropertyNamedRefIsNotASchemaRef(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","input":"go","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"$ref":{"type":"string"},"definitions":{"type":"string"}}}}]}`)

	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)

	assert.False(t, converted.Requirements.NativeOnly)
	assert.Equal(t, "string", gjson.GetBytes(converted.Body, `tools.0.function.parameters.properties.\$ref.type`).Str)
}

const toolArgsOnlyOnArgumentsDone = `event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"Read","arguments":"","status":"in_progress"}}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"path\":\"x.go\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"Read","arguments":"","status":"completed"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[{"id":"fc_1","type":"function_call","call_id":"call_a","name":"Read","arguments":""}],"usage":{"input_tokens":10,"output_tokens":5}}}

`

func TestResponsesToAnthropicWriter_ToolArgsFromArgumentsDoneEvent(t *testing.T) {
	rec, err := translateStream(t, responsesTranslators["anthropic"], true, toolArgsOnlyOnArgumentsDone)
	require.NoError(t, err)

	assert.Contains(t, rec.Body.String(), `"partial_json":"{\"path\":\"x.go\"}"`)
}

func TestResponsesToOpenAIChatWriter_ToolArgsFromArgumentsDoneEvent(t *testing.T) {
	rec, err := translateStream(t, responsesTranslators["openai_chat"], true, toolArgsOnlyOnArgumentsDone)
	require.NoError(t, err)

	assert.Contains(t, rec.Body.String(), `"arguments":"{\"path\":\"x.go\"}"`)
}

const textWithDroppedNamelessTool = `event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc_x","type":"function_call","call_id":"call_x","name":"","arguments":"","status":"in_progress"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]},{"id":"fc_x","type":"function_call","call_id":"call_x","name":"","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":1}}}

`

func TestResponsesToAnthropicWriter_TerminalToolClaimWithoutToolBlockEndsTurn(t *testing.T) {
	rec, err := translateStream(t, responsesTranslators["anthropic"], true, textWithDroppedNamelessTool)
	require.NoError(t, err)

	assert.Contains(t, rec.Body.String(), `"stop_reason":"end_turn"`)
	assert.NotContains(t, rec.Body.String(), `"stop_reason":"tool_use"`)
}

func TestResponsesToOpenAIChatWriter_TerminalToolClaimWithoutToolCallStops(t *testing.T) {
	rec, err := translateStream(t, responsesTranslators["openai_chat"], true, textWithDroppedNamelessTool)
	require.NoError(t, err)

	assert.Contains(t, rec.Body.String(), `"finish_reason":"stop"`)
	assert.NotContains(t, rec.Body.String(), `"finish_reason":"tool_calls"`)
}

func TestResponsesToAnthropicWriter_DoneOnlyItemStreamsWithoutTerminalOutput(t *testing.T) {
	const stream = `event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"final answer"}]}}

event: response.completed
data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":3}}}

`
	rec, err := translateStream(t, responsesTranslators["anthropic"], true, stream)
	require.NoError(t, err)

	assert.Contains(t, rec.Body.String(), `"text_delta","text":"final answer"`)
}

func TestResponsesWriter_NativeFailureSequenceFollowsHighestSeenSequence(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.5")
	w.SetPassthrough()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	native := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":7,\"delta\":\"a\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"delta\":\"b\"}\n\n"
	_, err := w.Write([]byte(native))
	require.NoError(t, err)
	require.NoError(t, w.FinalizeError(errors.New("upstream dropped")))

	events := parseSSEEvents(t, rec.Body.Bytes())
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, "response.failed", last["type"])
	assert.InDelta(t, 8, last["sequence_number"], 0)
}
