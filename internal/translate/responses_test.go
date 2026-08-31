package translate_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"workweave/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const codexResponsesBadgeSentinelForTest = "\u2063\u2060\u2063\u2060"

func TestResponsesToChatCompletions_InstructionsAndInput(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5",
		"instructions": "be helpful",
		"stream": true,
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]}
		]
	}`)

	out, isStream, model, err := translate.ResponsesToChatCompletions(body)
	require.NoError(t, err)
	assert.True(t, isStream)
	assert.Equal(t, "gpt-5", model)

	root := gjson.ParseBytes(out)
	assert.Equal(t, "gpt-5", root.Get("model").Str)
	assert.True(t, root.Get("stream").Bool())
	assert.True(t, root.Get("stream_options.include_usage").Bool())

	messages := root.Get("messages").Array()
	require.Len(t, messages, 2)
	assert.Equal(t, "system", messages[0].Get("role").Str)
	assert.Equal(t, "be helpful", messages[0].Get("content").Str)
	assert.Equal(t, "user", messages[1].Get("role").Str)
	assert.Equal(t, "hi", messages[1].Get("content").Str)
}

func TestResponsesToChatCompletions_FunctionCallRoundTrip(t *testing.T) {
	// Codex re-sends prior tool calls + their outputs in the input array.
	body := []byte(`{
		"model": "gpt-5",
		"input": [
			{"type": "message", "role": "user", "content": "do the thing"},
			{"type": "function_call", "call_id": "call_123", "name": "do_thing", "arguments": "{\"x\":1}"},
			{"type": "function_call_output", "call_id": "call_123", "output": "done"}
		]
	}`)

	out, _, _, err := translate.ResponsesToChatCompletions(body)
	require.NoError(t, err)

	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 3)

	// Assistant function_call → assistant message with tool_calls.
	assert.Equal(t, "assistant", messages[1].Get("role").Str)
	tc := messages[1].Get("tool_calls.0")
	assert.Equal(t, "call_123", tc.Get("id").Str)
	assert.Equal(t, "do_thing", tc.Get("function.name").Str)
	assert.Equal(t, `{"x":1}`, tc.Get("function.arguments").Str)

	// function_call_output → tool role message keyed by tool_call_id.
	assert.Equal(t, "tool", messages[2].Get("role").Str)
	assert.Equal(t, "call_123", messages[2].Get("tool_call_id").Str)
	assert.Equal(t, "done", messages[2].Get("content").Str)
}

func TestResponsesToChatCompletions_ToolsFlatToNested(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5",
		"input": "hi",
		"tools": [
			{"type": "function", "name": "search", "description": "search docs", "parameters": {"type": "object"}}
		]
	}`)

	out, _, _, err := translate.ResponsesToChatCompletions(body)
	require.NoError(t, err)

	tools := gjson.GetBytes(out, "tools").Array()
	require.Len(t, tools, 1)
	assert.Equal(t, "function", tools[0].Get("type").Str)
	assert.Equal(t, "search", tools[0].Get("function.name").Str)
	assert.Equal(t, "search docs", tools[0].Get("function.description").Str)
	assert.True(t, tools[0].Get("function.parameters").IsObject())
}

func TestConvertResponsesToChatCompletions_CustomToolStaysNative(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5",
		"input":"use the tool",
		"tools":[{"type":"custom","name":"apply_patch","description":"opaque payload"}]
	}`)

	converted, err := translate.ConvertResponsesToChatCompletions(body)
	require.NoError(t, err)
	assert.True(t, converted.Requirements.NativeOnly)
	assert.True(t, converted.Requirements.CustomTools)
	assert.Equal(t, body, converted.OriginalBody, "native dispatch must retain unknown tool bytes verbatim")
	assert.Len(t, converted.Report, 1)
	assert.Equal(t, "responses_non_function_tool_native_only", converted.Report[0].Code)
	assert.Empty(t, gjson.GetBytes(converted.Body, "tools").Array(), "routing projection must not pretend a custom tool is a function")
}

func TestConvertResponsesToChatCompletions_UnknownInputStaysNative(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5",
		"input":[{"type":"computer_call","id":"comp_1","action":{"type":"click"}}]
	}`)

	converted, err := translate.ConvertResponsesToChatCompletions(body)
	require.NoError(t, err)
	assert.True(t, converted.Requirements.NativeOnly)
	require.Len(t, converted.Report, 1)
	assert.Equal(t, "responses_unknown_input_native_only", converted.Report[0].Code)
}

func TestResponsesToChatCompletions_ToolChoiceRequiresTools(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5",
		"input": "hi",
		"tool_choice": "auto"
	}`)

	out, _, _, err := translate.ResponsesToChatCompletions(body)
	require.NoError(t, err)
	assert.False(t, gjson.GetBytes(out, "tool_choice").Exists())
}

func TestResponsesToChatCompletions_StripsRoutingBadgeFromAssistantHistory(t *testing.T) {
	// The egress badge must not survive ingress, or repeated turns leak
	// router bytes that break prompt-cache reuse.
	body := []byte(`{
		"model": "gpt-5",
		"input": [
			{"type": "message", "role": "user", "content": "hi"},
			{"type": "message", "role": "assistant", "content": [
				{"type": "output_text", "text": "**WEAVE ROUTER** — claude-opus-4-7 ← gpt-5.5\n\nHello there!"}
			]},
			{"type": "message", "role": "user", "content": "again"}
		]
	}`)

	out, _, _, err := translate.ResponsesToChatCompletions(body)
	require.NoError(t, err)

	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 3)
	assert.Equal(t, "assistant", messages[1].Get("role").Str)
	assert.Equal(t, "Hello there!", messages[1].Get("content").Str)
}

func TestResponsesToChatCompletions_StripsVerboseRoutingMarker(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5",
		"input": [
			{"type": "message", "role": "assistant", "content": "✦ **WEAVE ROUTER** → minimax/minimax-m3 · best pick for this turn\n↳ classifier balanced\n↳ bandit arm minimax\n\nbody"}
		]
	}`)

	out, _, _, err := translate.ResponsesToChatCompletions(body)
	require.NoError(t, err)

	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 1)
	assert.Equal(t, "body", messages[0].Get("content").Str)
}

func TestResponsesToChatCompletions_StripsBadgeFromStringContent(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5",
		"input": [
			{"type": "message", "role": "assistant", "content": "**WEAVE ROUTER** — claude-opus-4-7\n\nbody"}
		]
	}`)

	out, _, _, err := translate.ResponsesToChatCompletions(body)
	require.NoError(t, err)

	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 1)
	assert.Equal(t, "body", messages[0].Get("content").Str)
}

func TestResponsesToChatCompletions_LeavesUserContentAlone(t *testing.T) {
	// User content that happens to start with the marker bytes (e.g. someone
	// pasting our log line in) must not be silently mutated.
	body := []byte(`{
		"model": "gpt-5",
		"input": [
			{"type": "message", "role": "user", "content": "**WEAVE ROUTER** — something\n\nplease explain"}
		]
	}`)

	out, _, _, err := translate.ResponsesToChatCompletions(body)
	require.NoError(t, err)

	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 1)
	assert.Contains(t, messages[0].Get("content").Str, "**WEAVE ROUTER**")
}

func TestStripRoutingBadgeFromResponsesInput_PreservesNativeFields(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"**WEAVE ROUTER** — quoted\n\ndo not strip"}]},
			{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"},
			{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[
				{"type":"refusal","refusal":""},
				{"type":"output_text","text":"\u2063\u2060\u2063\u2060**Weave Router** — gpt-5.6-terra ← gpt-5.6-sol\n\nanswer","annotations":[{"type":"url_citation","url":"https://example.com"}]},
				{"type":"output_text","text":"**WEAVE ROUTER** — user-authored\n\nlater part"}
			]},
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"x\":1}"}
		],
		"metadata":{"keep":true}
	}`)

	out, err := translate.StripRoutingBadgeFromResponsesInput(body)
	require.NoError(t, err)

	root := gjson.ParseBytes(out)
	assert.Equal(t, "**WEAVE ROUTER** — quoted\n\ndo not strip", root.Get("input.0.content.0.text").Str)
	assert.Equal(t, "opaque", root.Get("input.1.encrypted_content").Str)
	assert.Equal(t, "answer", root.Get("input.2.content.1.text").Str)
	assert.Equal(t, "https://example.com", root.Get("input.2.content.1.annotations.0.url").Str)
	assert.Equal(t, "**WEAVE ROUTER** — user-authored\n\nlater part", root.Get("input.2.content.2.text").Str)
	assert.Equal(t, "call_1", root.Get("input.3.call_id").Str)
	assert.True(t, root.Get("metadata.keep").Bool())
}

func TestStripRouterCommandsFromResponsesInput_RemovesAgentToolCommand(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"custom_tool_call_output","call_id":"call_skill","output":" /router-feedback too slow"},{"type":"function_call_output","call_id":"call_fm","output":[{"type":"input_text","text":" /force-model gpt-5"}]}]}`)
	out, err := translate.StripRouterCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, "", gjson.GetBytes(out, "input.0.output").String())
	assert.Equal(t, "", gjson.GetBytes(out, "input.1.output.0.text").String())
}

func TestStripRouterCommandsFromResponsesInput_RemovesCollapsedExecCommand(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call_output","call_id":"call_skill","output":"Script completed\nOutput:\n /force-model gpt-5"}]}`)
	out, err := translate.StripRouterCommandsFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, "Script completed\nOutput:", gjson.GetBytes(out, "input.0.output").String())
}

// A tool-call-only or reasoning-only turn ships a badge-only assistant message.
// Stripping it in place would leave a blank assistant shell ahead of the real
// function_call, which providers reject.
func TestStripRoutingBadgeFromResponsesInput_DropsBadgeOnlyAssistantItem(t *testing.T) {
	badge := `\u2063\u2060\u2063\u2060**Weave Router** — gpt-5.6-terra ← gpt-5.6-sol\n\n`
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{"type":"message","role":"user","content":"go"},
			{"type":"message","role":"assistant","content":"` + badge + `"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + badge + `"}]},
			{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"}
		]
	}`)

	out, err := translate.StripRoutingBadgeFromResponsesInput(body)
	require.NoError(t, err)

	input := gjson.GetBytes(out, "input").Array()
	require.Len(t, input, 2)
	assert.Equal(t, "user", input[0].Get("role").Str)
	assert.Equal(t, "call_1", input[1].Get("call_id").Str)
}

// Only the badge part is empty here — the item still carries content, so it stays.
func TestStripRoutingBadgeFromResponsesInput_KeepsItemWithRemainingContent(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{"type":"message","role":"assistant","content":[
				{"type":"output_text","text":"\u2063\u2060\u2063\u2060**Weave Router** — gpt-5.6-terra\n\n"},
				{"type":"output_text","text":"the answer"}
			]}
		]
	}`)

	out, err := translate.StripRoutingBadgeFromResponsesInput(body)
	require.NoError(t, err)

	input := gjson.GetBytes(out, "input").Array()
	require.Len(t, input, 1)
	assert.Equal(t, "", input[0].Get("content.0.text").Str)
	assert.Equal(t, "the answer", input[0].Get("content.1.text").Str)
}

// Defense in depth for clients that echo an already-emptied assistant message.
func TestResponsesToChatCompletions_DropsEmptyAssistantShell(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{"type":"message","role":"user","content":"go"},
			{"type":"message","role":"assistant","content":""},
			{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"}
		]
	}`)

	out, _, _, err := translate.ResponsesToChatCompletions(body)
	require.NoError(t, err)

	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 2)
	assert.Equal(t, "user", messages[0].Get("role").Str)
	assert.Equal(t, "call_1", messages[1].Get("tool_calls.0.id").Str)
}

func TestStripRoutingBadgeFromResponsesInput_NoMatchPreservesBytes(t *testing.T) {
	body := []byte("{ \"model\" : \"gpt-5.6-sol\", \"input\" : [{\"type\":\"message\",\"role\":\"assistant\",\"content\":\"plain answer\"}] }\n")

	out, err := translate.StripRoutingBadgeFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, body, out)
}

func TestStripRoutingBadgeFromResponsesInput_PreservesOrganicAssistantHeading(t *testing.T) {
	body := []byte("{ \"model\" : \"gpt-5.6-sol\", \"input\" : [{\"type\":\"message\",\"role\":\"assistant\",\"content\":\"**WEAVE ROUTER** — an ordinary heading\\n\\nkeep this paragraph\"}] }\n")

	out, err := translate.StripRoutingBadgeFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, body, out)
}

func TestResponsesToChatCompletions_MaxOutputAndDropsReasoning(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5",
		"input": "hi",
		"max_output_tokens": 4096,
		"reasoning": {"effort": "high"}
	}`)

	out, _, _, err := translate.ResponsesToChatCompletions(body)
	require.NoError(t, err)

	assert.Equal(t, int64(4096), gjson.GetBytes(out, "max_completion_tokens").Int())
	// reasoning is intentionally dropped pre-routing: forwarding it as
	// reasoning_effort broke every non-Gemini served model.
	assert.False(t, gjson.GetBytes(out, "reasoning_effort").Exists(),
		"reasoning_effort must not be propagated into the chat body")
	assert.False(t, gjson.GetBytes(out, "reasoning").Exists())
}

// Codex always sends a `reasoning` field, including effort:"none" (invalid as
// reasoning_effort); reasoning OpenAI models also 400 on reasoning_effort+tools.
// No effort value may leak into the translated chat body.
func TestResponsesToChatCompletions_DropsReasoningEffort(t *testing.T) {
	for _, effort := range []string{"none", "minimal", "low", "medium", "high"} {
		body := []byte(`{"model":"gpt-5.5","input":"hi","reasoning":{"effort":"` + effort + `"},"include":["reasoning.encrypted_content"]}`)
		out, _, _, err := translate.ResponsesToChatCompletions(body)
		require.NoError(t, err)
		assert.Falsef(t, gjson.GetBytes(out, "reasoning_effort").Exists(),
			"effort %q must not propagate", effort)
	}
}

// Once response.created is on the wire, an upstream error must terminate with
// response.failed, never a bare disconnect (Codex reports that as "stream
// closed before response.completed").
func TestResponsesWriter_FinalizeErrorEmitsFailed(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "")

	require.NoError(t, w.Prelude(true)) // emits response.created
	require.NoError(t, w.FinalizeError(errors.New("upstream 400")))

	events := parseSSEEvents(t, rec.Body.Bytes())
	types := eventTypes(events)
	assert.Contains(t, types, "response.created")
	assert.Contains(t, types, "response.failed")
	assert.NotContains(t, types, "response.completed")

	final := events[len(events)-1]
	require.Equal(t, "response.failed", final["type"])
	resp := final["response"].(map[string]any)
	assert.Equal(t, "failed", resp["status"])
	assert.NotNil(t, resp["error"])
}

// Before anything streams, FinalizeError writes nothing so the handler can
// still emit a JSON error envelope.
func TestResponsesWriter_FinalizeErrorNoopBeforeCreated(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "")

	require.NoError(t, w.FinalizeError(errors.New("upstream 400")))
	assert.Empty(t, rec.Body.Bytes())
}

func TestResponsesWriter_StreamingText(t *testing.T) {
	rec := httptest.NewRecorder()
	// No model / x-router-model header, so the badge stays silent and the
	// test can focus on chunk translation.
	w := translate.NewResponsesWriter(rec, "")

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)

	// Simulate chat-completions chunks from the existing path.
	chunks := []string{
		`data: {"id":"c1","object":"chat.completion.chunk","model":"gpt-5","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	for _, c := range chunks {
		_, err := w.Write([]byte(c))
		require.NoError(t, err)
	}
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())
	types := eventTypes(events)
	assert.Contains(t, types, "response.created")
	assert.Contains(t, types, "response.output_item.added")
	assert.Contains(t, types, "response.content_part.added")
	assert.Contains(t, types, "response.output_text.delta")
	assert.Contains(t, types, "response.output_text.done")
	assert.Contains(t, types, "response.content_part.done")
	assert.Contains(t, types, "response.output_item.done")
	assert.Contains(t, types, "response.completed")

	// Skip the badge prefix the writer prepends on the first delta (model
	// "gpt-5" resolves here so the badge fires).
	var combined strings.Builder
	for _, e := range events {
		if e["type"] != "response.output_text.delta" {
			continue
		}
		d := e["delta"].(string)
		if strings.HasPrefix(d, "**Weave Router**") {
			continue
		}
		combined.WriteString(d)
	}
	assert.Equal(t, "Hello world", combined.String())

	// Final completed event carries usage.
	final := events[len(events)-1]
	assert.Equal(t, "response.completed", final["type"])
	usage := final["response"].(map[string]any)["usage"].(map[string]any)
	assert.EqualValues(t, 3, usage["input_tokens"])
	assert.EqualValues(t, 2, usage["output_tokens"])
}

// When upstream already speaks Responses natively, the writer must forward
// bytes unchanged and skip its own response.created prelude.
func TestResponsesWriter_PassthroughForwardsVerbatim(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.5")
	w.SetPassthrough()

	// Prelude is a no-op in passthrough — the upstream emits response.created.
	require.NoError(t, w.Prelude(true))

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)

	// Verbatim Responses SSE, as the Codex backend emits it.
	native := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"
	_, err := w.Write([]byte(native))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	// Output is exactly the upstream bytes: no chat->Responses translation, no
	// synthesized or duplicated events.
	assert.Equal(t, native, rec.Body.String())
}

// passthroughTestMarker stands in for the routing marker the proxy supplies.
const passthroughTestMarker = "✦ **Weave Router** → gpt-5.6-terra · best pick for this turn"

func TestResponsesWriter_PassthroughBadgePreservesNativeStream(t *testing.T) {
	for _, terminalType := range []string{"response.completed", "response.incomplete"} {
		t.Run(terminalType, func(t *testing.T) {
			payloads := []string{
				`{"type":"response.created","sequence_number":0,"response":{"id":"resp_native","model":"gpt-5.6-terra","status":"in_progress","output":[]}}`,
				`{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_native","model":"gpt-5.6-terra","status":"in_progress","output":[]}}`,
				`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_native","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
				`{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_native","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
				`{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_native","output_index":0,"content_index":0,"delta":"o"}`,
				`{"type":"response.output_text.delta","sequence_number":5,"item_id":"msg_native","output_index":0,"content_index":0,"delta":"k"}`,
				`{"type":"response.output_text.done","sequence_number":6,"item_id":"msg_native","output_index":0,"content_index":0,"text":"ok"}`,
				`{"type":"response.content_part.done","sequence_number":7,"item_id":"msg_native","output_index":0,"content_index":0,"part":{"type":"output_text","text":"ok","annotations":[]}}`,
				`{"type":"response.output_item.done","sequence_number":8,"output_index":0,"item":{"id":"msg_native","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}}`,
				`{"type":"response.output_item.added","sequence_number":9,"output_index":1,"item":{"id":"fc_native","type":"function_call","call_id":"call_native","name":"lookup","arguments":"","status":"in_progress"}}`,
				`{"type":"response.function_call_arguments.delta","sequence_number":10,"item_id":"fc_native","output_index":1,"delta":"{\"x\":1}"}`,
				`{"type":"response.function_call_arguments.done","sequence_number":11,"item_id":"fc_native","output_index":1,"arguments":"{\"x\":1}"}`,
				`{"type":"response.output_item.done","sequence_number":12,"output_index":1,"item":{"id":"fc_native","type":"function_call","call_id":"call_native","name":"lookup","arguments":"{\"x\":1}","status":"completed"}}`,
				`{"type":"` + terminalType + `","sequence_number":13,"response":{"id":"resp_native","model":"gpt-5.6-terra","status":"completed","output":[{"id":"msg_native","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]},{"id":"fc_native","type":"function_call","call_id":"call_native","name":"lookup","arguments":"{\"x\":1}","status":"completed"}]}}`,
			}
			var native strings.Builder
			for _, payload := range payloads {
				native.WriteString("event: " + gjson.Get(payload, "type").Str + "\n")
				native.WriteString("data: " + payload + "\n\n")
			}

			rec := httptest.NewRecorder()
			w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
			w.SetBadgeText(passthroughTestMarker)
			w.SetPassthroughBadge()
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("x-router-model", "gpt-5.6-terra")
			w.WriteHeader(200)

			raw := []byte(native.String())
			for start := 0; start < len(raw); start += 37 {
				end := min(start+37, len(raw))
				_, err := w.Write(raw[start:end])
				require.NoError(t, err)
			}
			require.NoError(t, w.Finalize())

			events := parseSSEEvents(t, rec.Body.Bytes())
			require.Len(t, events, len(payloads))
			for index, event := range events {
				assert.EqualValues(t, index, event["sequence_number"], "sequence number at event %d", index)
				assert.Equal(t, gjson.Get(payloads[index], "type").Str, event["type"])
			}

			badge := codexResponsesBadgeSentinelForTest + passthroughTestMarker + "\n\n"
			assert.Equal(t, badge+"o", events[4]["delta"], "only the first text delta gets the badge")
			assert.Equal(t, "k", events[5]["delta"], "later deltas stay native")
			assert.Equal(t, badge+"ok", events[6]["text"])
			assert.Equal(t, badge+"ok", events[7]["part"].(map[string]any)["text"])
			assert.Equal(t, badge+"ok", events[8]["item"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
			response := events[13]["response"].(map[string]any)
			assert.Equal(t, "resp_native", response["id"])
			assert.Equal(t, "gpt-5.6-terra", response["model"])
			output := response["output"].([]any)
			assert.Equal(t, badge+"ok", output[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])

			// Tool events and the completed aggregate tool item stay untouched.
			for _, index := range []int{9, 10, 11, 12} {
				var original map[string]any
				require.NoError(t, json.Unmarshal([]byte(payloads[index]), &original))
				assert.Equal(t, original, events[index])
			}
			var originalTerminal map[string]any
			require.NoError(t, json.Unmarshal([]byte(payloads[13]), &originalTerminal))
			originalTool := originalTerminal["response"].(map[string]any)["output"].([]any)[1]
			assert.Equal(t, originalTool, output[1])
		})
	}
}

func TestResponsesWriter_PassthroughBadgeCanGrowPastOriginalEventCapacity(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	badge := strings.Repeat("routed-model ", 511) + "routed-model"
	w.SetBadgeText(badge)
	w.SetPassthroughBadge()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("x-router-model", "gpt-5.6-terra")
	w.WriteHeader(200)

	native := `event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_native","output_index":0,"content_index":0,"delta":"ok"}

`
	_, err := w.Write([]byte(native))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())
	require.Len(t, events, 1)
	assert.Equal(t, codexResponsesBadgeSentinelForTest+badge+"\n\nok", events[0]["delta"])
}

func TestResponsesWriter_PassthroughBadgeEmitsNativeItemForToolOnlyTurn(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	w.SetBadgeText(passthroughTestMarker)
	w.SetPassthroughBadge()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)

	native := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_native","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_native","type":"function_call","call_id":"call_native","name":"lookup","arguments":"","status":"in_progress"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_native","output_index":0,"delta":"{\"x\":1}"}`,
		`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"id":"fc_native","type":"function_call","call_id":"call_native","name":"lookup","arguments":"{\"x\":1}","status":"completed"}}`,
		`{"type":"response.completed","sequence_number":4,"response":{"id":"resp_native","status":"completed","output":[{"id":"fc_native","type":"function_call","call_id":"call_native","name":"lookup","arguments":"{\"x\":1}","status":"completed"}]}}`,
	}
	var stream strings.Builder
	for _, payload := range native {
		stream.WriteString("event: " + gjson.Get(payload, "type").Str + "\ndata: " + payload + "\n\n")
	}
	_, err := w.Write([]byte(stream.String()))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())
	require.Len(t, events, len(native)+6)
	for i, event := range events {
		if i == 0 {
			assert.EqualValues(t, 0, event["sequence_number"])
			continue
		}
		assert.EqualValues(t, i, event["sequence_number"])
	}
	assert.Equal(t, "message", events[1]["item"].(map[string]any)["type"])
	assert.Equal(t, codexResponsesBadgeSentinelForTest+passthroughTestMarker+"\n\n", events[3]["delta"])
	assert.EqualValues(t, 0, events[1]["output_index"])
	assert.EqualValues(t, 1, events[7]["output_index"])
	completed := events[len(events)-1]["response"].(map[string]any)
	output := completed["output"].([]any)
	require.Len(t, output, 2)
	assert.Equal(t, "message", output[0].(map[string]any)["type"])
	assert.Equal(t, "function_call", output[1].(map[string]any)["type"])
}

func TestResponsesWriter_PassthroughBadgeRewritesNonStreamingBody(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	w.SetBadgeText(passthroughTestMarker)
	w.SetPassthroughBadge()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, err := w.Write([]byte(`{"id":"resp_native","output":[{"id":"msg_native","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	output := body["output"].([]any)
	text := output[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	assert.Equal(t, codexResponsesBadgeSentinelForTest+passthroughTestMarker+"\n\nok", text)
}

func TestResponsesWriter_PrependsRoutingMarkerBadge(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.5")
	w.SetBadgeText(passthroughTestMarker)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("x-router-model", "claude-opus-4-7")
	w.WriteHeader(200)

	for _, c := range []string{
		`data: {"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n",
		"data: [DONE]\n\n",
	} {
		_, err := w.Write([]byte(c))
		require.NoError(t, err)
	}
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())

	var deltas []string
	for _, e := range events {
		if e["type"] == "response.output_text.delta" {
			deltas = append(deltas, e["delta"].(string))
		}
	}
	require.Len(t, deltas, 3)
	assert.Equal(t, passthroughTestMarker+"\n\n", deltas[0])
	assert.Equal(t, "Hello", deltas[1])
	assert.Equal(t, " world", deltas[2])

	// response.completed appears exactly once.
	completedCount := 0
	for _, e := range events {
		if e["type"] == "response.completed" {
			completedCount++
		}
	}
	assert.Equal(t, 1, completedCount)
}

// Suppression is decided by the proxy (opt-out header, same-model turn, hidden surfaces);
// no marker supplied → no badge emitted.
func TestResponsesWriter_WithoutMarkerEmitsNoBadge(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.5")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("x-router-model", "claude-opus-4-7")
	w.WriteHeader(200)

	_, err := w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n"))
	require.NoError(t, err)
	_, err = w.Write([]byte("data: [DONE]\n\n"))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())
	var deltas []string
	for _, e := range events {
		if e["type"] == "response.output_text.delta" {
			deltas = append(deltas, e["delta"].(string))
		}
	}
	assert.Equal(t, []string{"hi"}, deltas)
}

// A Codex action that only calls tools produces no assistant text of its own,
// so the badge has to open its own message item or it never renders.
func TestResponsesWriter_EmitsBadgeOnToolCallOnlyTurn(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	w.SetBadgeText(passthroughTestMarker)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("x-router-model", "claude-opus-4-7")
	w.WriteHeader(200)

	for _, c := range []string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"shell","arguments":"{\"cmd\":"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n",
		"data: [DONE]\n\n",
	} {
		_, err := w.Write([]byte(c))
		require.NoError(t, err)
	}
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())

	var deltas []string
	for _, e := range events {
		if e["type"] == "response.output_text.delta" {
			deltas = append(deltas, e["delta"].(string))
		}
	}
	require.Equal(t, []string{passthroughTestMarker + "\n\n"}, deltas)

	// The badge message must precede the tool call and carry a lower output
	// index, else Codex renders it after the command it announces.
	var completed map[string]any
	for _, e := range events {
		if e["type"] == "response.completed" {
			completed = e["response"].(map[string]any)
		}
	}
	require.NotNil(t, completed)
	output := completed["output"].([]any)
	require.Len(t, output, 2)
	message := output[0].(map[string]any)
	assert.Equal(t, "message", message["type"])
	assert.Equal(t, passthroughTestMarker+"\n\n", message["content"].([]any)[0].(map[string]any)["text"])
	assert.Equal(t, "function_call", output[1].(map[string]any)["type"])
	assert.Equal(t, `{"cmd":"ls"}`, output[1].(map[string]any)["arguments"])
}

// Reasoning deltas are not translated into output items, so a reasoning-only
// turn reaches finish with nothing that would otherwise pull in the badge.
func TestResponsesWriter_EmitsBadgeOnReasoningOnlyTurn(t *testing.T) {
	for _, field := range []string{"reasoning", "reasoning_content"} {
		t.Run(field, func(t *testing.T) {
			rec := httptest.NewRecorder()
			w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
			w.SetBadgeText(passthroughTestMarker)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)

			for _, c := range []string{
				`data: {"choices":[{"index":0,"delta":{"` + field + `":"thinking"},"finish_reason":null}]}` + "\n\n",
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
				"data: [DONE]\n\n",
			} {
				_, err := w.Write([]byte(c))
				require.NoError(t, err)
			}
			require.NoError(t, w.Finalize())

			events := parseSSEEvents(t, rec.Body.Bytes())

			var deltas []string
			var completed map[string]any
			for _, e := range events {
				switch e["type"] {
				case "response.output_text.delta":
					deltas = append(deltas, e["delta"].(string))
				case "response.completed":
					completed = e["response"].(map[string]any)
				}
			}
			require.Equal(t, []string{passthroughTestMarker + "\n\n"}, deltas)
			require.NotNil(t, completed)
			output := completed["output"].([]any)
			require.Len(t, output, 1)
			assert.Equal(t, passthroughTestMarker+"\n\n",
				output[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
		})
	}
}

func TestResponsesWriter_UsesRoutedModelFromHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5")

	// Simulate the proxy stamping its routing decision on the writer headers
	// before any body bytes flow through.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("x-router-model", "claude-opus-4-7")
	w.Header().Set("x-router-provider", "anthropic")
	w.WriteHeader(200)

	_, err := w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n"))
	require.NoError(t, err)
	_, err = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n"))
	require.NoError(t, err)
	_, err = w.Write([]byte("data: [DONE]\n\n"))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())

	// response.created and response.completed both carry the routed model.
	var created, completed map[string]any
	for _, e := range events {
		switch e["type"] {
		case "response.created":
			created = e["response"].(map[string]any)
		case "response.completed":
			completed = e["response"].(map[string]any)
		}
	}
	require.NotNil(t, created)
	require.NotNil(t, completed)
	assert.Equal(t, "claude-opus-4-7", created["model"])
	assert.Equal(t, "claude-opus-4-7", completed["model"])
}

func TestResponsesWriter_UsesCustomBadgeText(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5")
	w.SetBadgeText("✦ **Weave Router** → minimax/minimax-m3 · best pick for this turn\n↳ classifier balanced")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("x-router-model", "minimax/minimax-m3")
	w.WriteHeader(200)

	_, err := w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n"))
	require.NoError(t, err)
	_, err = w.Write([]byte("data: [DONE]\n\n"))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())
	var firstDelta string
	for _, e := range events {
		if e["type"] == "response.output_text.delta" {
			firstDelta = e["delta"].(string)
			break
		}
	}
	assert.Contains(t, firstDelta, "best pick for this turn")
	assert.Contains(t, firstDelta, "↳ classifier balanced")
	assert.True(t, strings.HasSuffix(firstDelta, "\n\n"))
}

func TestResponsesWriter_StreamingToolCall(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5")
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)

	chunks := []string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_a","function":{"name":"do","arguments":""}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	for _, c := range chunks {
		_, err := w.Write([]byte(c))
		require.NoError(t, err)
	}
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())
	types := eventTypes(events)
	assert.Contains(t, types, "response.output_item.added")
	assert.Contains(t, types, "response.function_call_arguments.delta")
	assert.Contains(t, types, "response.function_call_arguments.done")
	assert.Contains(t, types, "response.completed")

	// Args reassembled.
	var args strings.Builder
	for _, e := range events {
		if e["type"] == "response.function_call_arguments.delta" {
			args.WriteString(e["delta"].(string))
		}
	}
	assert.Equal(t, `{"x":1}`, args.String())

	// Final item carries call_id and full arguments.
	for _, e := range events {
		if e["type"] == "response.function_call_arguments.done" {
			assert.Equal(t, `{"x":1}`, e["arguments"])
		}
	}
}

func TestResponsesWriter_NonContiguousToolCallIndices(t *testing.T) {
	// Upstream sends two tool calls with indices 0 and 2 (gap at 1); both
	// must appear in the response.completed output.
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5")
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)

	chunks := []string{
		// Tool call at index=0 (search)
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_a","function":{"name":"search","arguments":""}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"x\"}"}}]},"finish_reason":null}]}` + "\n\n",
		// Tool call at index=2 (gap at 1 — simulates non-contiguous upstream)
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"id":"call_b","function":{"name":"lookup","arguments":""}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"function":{"arguments":"{\"id\":1}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	for _, c := range chunks {
		_, err := w.Write([]byte(c))
		require.NoError(t, err)
	}
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())

	// Find the response.completed event.
	var completed map[string]any
	for _, e := range events {
		if e["type"] == "response.completed" {
			completed = e
			break
		}
	}
	require.NotNil(t, completed, "response.completed event must be present")

	response := completed["response"].(map[string]any)
	output := response["output"].([]any)

	// Both tool calls must appear in output — not just the first one.
	require.Len(t, output, 2, "both tool calls must appear in output; got %d", len(output))

	// Verify each tool call by name (order: index 0 then index 2).
	first := output[0].(map[string]any)
	assert.Equal(t, "search", first["name"], "first tool call must be 'search'")
	assert.Equal(t, `{"q":"x"}`, first["arguments"])

	second := output[1].(map[string]any)
	assert.Equal(t, "lookup", second["name"], "second tool call must be 'lookup'")
	assert.Equal(t, `{"id":1}`, second["arguments"])
}

func parseSSEEvents(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var events []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &m))
		events = append(events, m)
	}
	return events
}

func eventTypes(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		if s, ok := e["type"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestResponsesWriter_PassthroughAppendsFeedbackFooter(t *testing.T) {
	const footer = "\n\n_Weave Router feedback:_ `$rf +` good experience · `$rf -` poor experience"
	payloads := []string{
		`{"type":"response.output_text.delta","item_id":"msg_native","output_index":0,"content_index":0,"delta":"ok"}`,
		`{"type":"response.output_text.done","item_id":"msg_native","output_index":0,"content_index":0,"text":"ok"}`,
		`{"type":"response.completed","response":{"id":"resp_native","status":"completed","output":[{"id":"msg_native","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`,
	}
	var native strings.Builder
	for _, payload := range payloads {
		native.WriteString("event: " + gjson.Get(payload, "type").Str + "\n")
		native.WriteString("data: " + payload + "\n\n")
	}

	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	w.SetFooterText(footer)
	w.SetPassthroughBadge()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	_, err := w.Write([]byte(native.String()))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())
	require.Len(t, events, 3)
	assert.Equal(t, "ok", events[0]["delta"])
	assert.Equal(t, "ok"+footer, events[1]["text"])
	output := events[2]["response"].(map[string]any)["output"].([]any)
	assert.Equal(t, "ok"+footer, output[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
}

func TestResponsesWriter_PassthroughReasoningThenTextKeepsBadgeAndFooter(t *testing.T) {
	const footer = "\n\n_Weave Router feedback:_ `$rf +` good"
	payloads := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_native","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"rs_native","type":"reasoning","status":"in_progress","summary":[]}}`,
		`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"id":"rs_native","type":"reasoning","status":"completed","summary":[]}}`,
		`{"type":"response.output_item.added","sequence_number":3,"output_index":1,"item":{"id":"msg_native","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":4,"item_id":"msg_native","output_index":1,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":5,"item_id":"msg_native","output_index":1,"content_index":0,"delta":"ok"}`,
		`{"type":"response.output_text.done","sequence_number":6,"item_id":"msg_native","output_index":1,"content_index":0,"text":"ok"}`,
		`{"type":"response.content_part.done","sequence_number":7,"item_id":"msg_native","output_index":1,"content_index":0,"part":{"type":"output_text","text":"ok","annotations":[]}}`,
		`{"type":"response.output_item.done","sequence_number":8,"output_index":1,"item":{"id":"msg_native","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}}`,
		`{"type":"response.completed","sequence_number":9,"response":{"id":"resp_native","status":"completed","output":[{"id":"rs_native","type":"reasoning","status":"completed","summary":[]},{"id":"msg_native","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}}`,
	}
	var native strings.Builder
	for _, payload := range payloads {
		native.WriteString("event: " + gjson.Get(payload, "type").Str + "\n")
		native.WriteString("data: " + payload + "\n\n")
	}

	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	w.SetBadgeText(passthroughTestMarker)
	w.SetFooterText(footer)
	w.SetPassthroughBadge()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	_, err := w.Write([]byte(native.String()))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	events := parseSSEEvents(t, rec.Body.Bytes())
	require.Len(t, events, len(payloads)+6)
	for i, event := range events {
		if i == 0 {
			assert.EqualValues(t, 0, event["sequence_number"])
			continue
		}
		assert.EqualValues(t, i, event["sequence_number"], "sequence at %d", i)
	}
	assert.Equal(t, "message", events[1]["item"].(map[string]any)["type"])
	assert.EqualValues(t, 0, events[1]["output_index"])
	badge := codexResponsesBadgeSentinelForTest + passthroughTestMarker + "\n\n"
	assert.Equal(t, badge, events[3]["delta"])
	assert.Equal(t, "reasoning", events[7]["item"].(map[string]any)["type"])
	assert.EqualValues(t, 1, events[7]["output_index"])
	assert.EqualValues(t, 2, events[9]["output_index"])
	assert.Equal(t, "ok", events[11]["delta"])
	assert.Equal(t, "ok"+footer, events[12]["text"])
	output := events[len(events)-1]["response"].(map[string]any)["output"].([]any)
	require.Len(t, output, 3)
	assert.Equal(t, "message", output[0].(map[string]any)["type"])
	assert.Equal(t, badge, output[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
	assert.Equal(t, "reasoning", output[1].(map[string]any)["type"])
	assert.Equal(t, "ok"+footer, output[2].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
}

func TestResponsesWriter_PassthroughFooterSkippedOnToolCall(t *testing.T) {
	const footer = "\n\n_Weave Router feedback:_ `$rf +` good"
	payloads := []string{
		`{"type":"response.output_text.delta","item_id":"msg_native","output_index":0,"content_index":0,"delta":"ok"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_native","type":"function_call","name":"lookup"}}`,
		`{"type":"response.output_text.done","item_id":"msg_native","output_index":0,"content_index":0,"text":"ok"}`,
	}
	var native strings.Builder
	for _, payload := range payloads {
		native.WriteString("event: " + gjson.Get(payload, "type").Str + "\n")
		native.WriteString("data: " + payload + "\n\n")
	}
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-sol")
	w.SetFooterText(footer)
	w.SetPassthroughBadge()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	_, err := w.Write([]byte(native.String()))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())
	events := parseSSEEvents(t, rec.Body.Bytes())
	require.Len(t, events, 3)
	assert.Equal(t, "ok", events[0]["delta"])
	assert.Equal(t, "ok", events[2]["text"])
}

func TestResponsesWriter_TranslatedStreamAppendsFeedbackFooter(t *testing.T) {
	const footer = "\n\n_Weave Router feedback:_ `$rf +` good"
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.5")
	w.SetFooterText(footer)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	for _, c := range []string{
		`data: {"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
		"data: [DONE]\n\n",
	} {
		_, err := w.Write([]byte(c))
		require.NoError(t, err)
	}
	require.NoError(t, w.Finalize())
	events := parseSSEEvents(t, rec.Body.Bytes())
	var deltas []string
	for _, e := range events {
		if e["type"] == "response.output_text.delta" {
			deltas = append(deltas, e["delta"].(string))
		}
	}
	require.GreaterOrEqual(t, len(deltas), 2)
	assert.Equal(t, "Hello", deltas[0])
	assert.Equal(t, footer, deltas[len(deltas)-1])
}

func TestStripFeedbackFooterFromResponsesInput(t *testing.T) {
	body := []byte("{\"input\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\\n\\n_Weave Router feedback:_ `$rf +` good experience · `$rf -` poor experience\"}]}]}")
	out, err := translate.StripFeedbackFooterFromResponsesInput(body)
	require.NoError(t, err)
	assert.Equal(t, "answer", gjson.GetBytes(out, "input.0.content.0.text").Str)
}

// Strip operates on passthrough bytes; extraction runs on the chat projection (conv.OriginalBody vs conv.Body). Ordered as ProxyOpenAIResponses does, so aliasing surfaces here.
func TestStripRouterCommandsFromResponsesInput_LeavesChatProjectionIntact(t *testing.T) {
	const body = `{"model":"gpt-5.6-terra","input":[
{"type":"message","role":"user","content":[{"type":"input_text","text":"invoke fm"}]},
{"type":"custom_tool_call","call_id":"call_a","name":"exec","input":"x"},
{"type":"custom_tool_call_output","call_id":"call_a","output":[
{"type":"input_text","text":"Script completed\nWall time 0.3 seconds\nOutput:\n"},
{"type":"input_text","text":" /force-model gpt-5.6-terra\n"}]}]}`

	conv, err := translate.ConvertResponsesToChatCompletionsWithOptions(
		[]byte(body), translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)

	native, err := translate.StripRouterCommandsFromResponsesInput(conv.OriginalBody)
	require.NoError(t, err)
	assert.NotContains(t, string(native), "/force-model gpt-5.6-terra",
		"the directive must not reach the upstream verbatim")

	env, err := translate.ParseOpenAI(conv.Body)
	require.NoError(t, err)
	res, found := env.ExtractForceModelCommand()
	require.True(t, found, "the chat projection must still carry the directive")
	assert.Equal(t, "gpt-5.6-terra", res.Model)
	assert.True(t, res.FromToolResult, "agent-issued, so the turn continues")
}

func TestConvertResponsesToChatCompletions_RejectsChatCompletionsBody(t *testing.T) {
	// Without this rejection the body reaches the upstream Responses endpoint verbatim as a 400.
	body := []byte(`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hi"}]}`)

	_, err := translate.ConvertResponsesToChatCompletions(body)

	require.Error(t, err)
	assert.ErrorIs(t, err, translate.ErrResponsesChatCompletionsBody)
}

func TestConvertResponsesToChatCompletionsWithOptions_PortableCodexRejectsChatCompletionsBody(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hi"}]}`)

	_, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})

	require.Error(t, err)
	assert.ErrorIs(t, err, translate.ErrResponsesChatCompletionsBody)
}

func TestConvertResponsesToChatCompletions_AcceptsInputOnlyBody(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)

	conv, err := translate.ConvertResponsesToChatCompletions(body)

	require.NoError(t, err)
	assert.Equal(t, "hi", gjson.GetBytes(conv.Body, "messages.0.content").String())
}
