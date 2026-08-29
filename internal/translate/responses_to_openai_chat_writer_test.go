package translate_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/translate"
	"workweave/router/internal/translate/toolcheck"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// chatChunks returns the parsed data payloads of a translated chat SSE stream,
// asserting every frame is a well-formed chat.completion.chunk and that the
// stream ends with [DONE].
func chatChunks(t *testing.T, body string) []gjson.Result {
	t.Helper()
	var out []gjson.Result
	sawDone := false
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			sawDone = true
			continue
		}
		require.False(t, sawDone, "no frame may follow [DONE]")
		require.True(t, gjson.Valid(data), "every frame must be valid JSON: %s", data)
		parsed := gjson.Parse(data)
		if parsed.Get("error").Exists() {
			out = append(out, parsed)
			continue
		}
		assert.Equal(t, "chat.completion.chunk", parsed.Get("object").String(), "frame: %s", data)
		assert.NotEmpty(t, parsed.Get("id").String())
		out = append(out, parsed)
	}
	assert.True(t, sawDone, "a chat client waits for data: [DONE]")
	return out
}

// concatDelta joins one delta field across all chunks.
func concatDelta(chunks []gjson.Result, field string) string {
	var sb strings.Builder
	for _, c := range chunks {
		sb.WriteString(c.Get("choices.0.delta." + field).String())
	}
	return sb.String()
}

// A streaming chat client must see text, reasoning, tool calls, usage, and
// [DONE] — the full translated Responses stream as chat chunks.
func TestResponsesToOpenAIChatWriter_StreamingClient(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)

	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(responsesStreamFixture))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	chunks := chatChunks(t, rec.Body.String())
	require.NotEmpty(t, chunks)

	assert.Equal(t, "assistant", chunks[0].Get("choices.0.delta.role").String(),
		"the role chunk commits the envelope while upstream is still reasoning")
	assert.Equal(t, "Checking the weather tool.", concatDelta(chunks, "reasoning_content"))
	assert.Equal(t, "Let me check the weather.", concatDelta(chunks, "content"))

	var toolCall gjson.Result
	for _, c := range chunks {
		if tc := c.Get("choices.0.delta.tool_calls.0"); tc.Exists() {
			toolCall = tc
		}
	}
	require.True(t, toolCall.Exists(), "the function_call must reach the client as a tool_calls delta")
	assert.EqualValues(t, 0, toolCall.Get("index").Int())
	assert.Equal(t, "call_xyz", toolCall.Get("id").String())
	assert.Equal(t, "function", toolCall.Get("type").String())
	assert.Equal(t, "get_weather", toolCall.Get("function.name").String())
	assert.Equal(t, `{"location":"NYC"}`, toolCall.Get("function.arguments").String(),
		"arguments are concatenated from both deltas and emitted as one JSON-encoded string")

	final := chunks[len(chunks)-1]
	assert.Equal(t, "tool_calls", final.Get("choices.0.finish_reason").String())
	assert.EqualValues(t, 150, final.Get("usage.prompt_tokens").Int())
	assert.EqualValues(t, 45, final.Get("usage.completion_tokens").Int())
	assert.EqualValues(t, 195, final.Get("usage.total_tokens").Int(),
		"Responses omits total_tokens here, so it must be derived")

	assert.Equal(t, "tool_calls", w.Summary().StopReason)
	assert.Equal(t, 1, w.Summary().ToolUseBlocks)
	assert.Equal(t, 45, w.Summary().OutputTokens)
}

// Usage details Responses reports must land on the chat detail objects, or
// cache-aware clients see a full-price prompt.
func TestResponsesToOpenAIChatWriter_StreamingUsageDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(`data: {"type":"response.output_text.delta","output_index":0,"delta":"hi"}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":80},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":12},"total_tokens":120}}}

`))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	chunks := chatChunks(t, rec.Body.String())
	final := chunks[len(chunks)-1]
	assert.EqualValues(t, 80, final.Get("usage.prompt_tokens_details.cached_tokens").Int())
	assert.EqualValues(t, 12, final.Get("usage.completion_tokens_details.reasoning_tokens").Int())
	assert.Equal(t, "stop", final.Get("choices.0.finish_reason").String())
	assert.Equal(t, 80, w.Summary().CacheReadTokens)
}

// A non-streaming client gets one chat.completion body built from the terminal
// event — no SSE frames.
func TestResponsesToOpenAIChatWriter_NonStreamingClient(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)

	require.NoError(t, w.Prelude(false))
	_, err := w.Write([]byte(responsesStreamFixture))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.NotContains(t, rec.Body.String(), "data: ", "a non-streaming client gets JSON, not SSE")

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "the body must be valid chat/completions JSON")
	root := gjson.ParseBytes(rec.Body.Bytes())
	assert.Equal(t, "chat.completion", root.Get("object").String())
	assert.Equal(t, "gpt-5.6-luna", root.Get("model").String())
	assert.Equal(t, "assistant", root.Get("choices.0.message.role").String())
	assert.Equal(t, "Let me check the weather.", root.Get("choices.0.message.content").String())
	assert.Equal(t, "Checking the weather tool.", root.Get("choices.0.message.reasoning_content").String())
	assert.Equal(t, "get_weather", root.Get("choices.0.message.tool_calls.0.function.name").String())
	assert.Equal(t, `{"location":"NYC"}`, root.Get("choices.0.message.tool_calls.0.function.arguments").String())
	assert.Equal(t, "call_xyz", root.Get("choices.0.message.tool_calls.0.id").String())
	assert.Equal(t, "tool_calls", root.Get("choices.0.finish_reason").String())
	assert.EqualValues(t, 150, root.Get("usage.prompt_tokens").Int())
	assert.EqualValues(t, 45, root.Get("usage.completion_tokens").Int())
}

// An output-token ceiling hit must surface as finish_reason "length" so the
// client knows the answer was truncated rather than complete.
func TestResponsesToOpenAIChatResponse_LengthFinishReason(t *testing.T) {
	out, err := translate.ResponsesToOpenAIChatResponse([]byte(`{"id":"resp_1","status":"incomplete",
      "incomplete_details":{"reason":"max_output_tokens"},
      "output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}]}`), "gpt-5.6-luna")
	require.NoError(t, err)
	root := gjson.ParseBytes(out)
	assert.Equal(t, "length", root.Get("choices.0.finish_reason").String())
	assert.Equal(t, "partial", root.Get("choices.0.message.content").String())
}

// The public helper passes no validator; malformed model args must degrade to
// {} rather than panic on the nil validator.
func TestResponsesToOpenAIChatResponse_NilValidatorMalformedArgs(t *testing.T) {
	out, err := translate.ResponsesToOpenAIChatResponse([]byte(`{"id":"resp_1","status":"completed",
      "output":[{"type":"function_call","call_id":"call_1","name":"bash","arguments":"{not json"}]}`), "gpt-5.6-luna")
	require.NoError(t, err)
	root := gjson.ParseBytes(out)
	assert.Equal(t, "{}", root.Get("choices.0.message.tool_calls.0.function.arguments").String())
	assert.Equal(t, "tool_calls", root.Get("choices.0.finish_reason").String())
}

// Args that violate the request schema are repaired against it, and the finding
// is surfaced so the proxy can log router.tool_call_invalid.
func TestResponsesToOpenAIChatWriter_ValidatesToolArgsAgainstRequestSchema(t *testing.T) {
	v := toolcheck.Compile([]byte(`[{"name":"get_weather","input_schema":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}}]`))
	require.NotNil(t, v)
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil).WithToolValidator(v)

	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"get_weather"}}

data: {"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"location\":\"NYC\",\"bogus\":1}"}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"location\":\"NYC\",\"bogus\":1}"}}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{}"}]}}

`))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	body := rec.Body.String()
	assert.Contains(t, body, `{\"location\":\"NYC\"}`, "the unknown key must be dropped by the schema repair")
	assert.NotEmpty(t, w.Summary().ToolCallIssues, "the finding must surface for router.tool_call_invalid")
}

// A nameless function_call must be dropped: a chat client would otherwise
// invoke tool "" in a loop.
func TestResponsesToOpenAIChatWriter_DropsNamelessToolCall(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":""}}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"","arguments":"{}"}}

data: {"type":"response.output_text.delta","output_index":1,"delta":"done"}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}

`))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	chunks := chatChunks(t, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "tool_calls\":[")
	assert.Equal(t, "done", concatDelta(chunks, "content"))
	assert.Equal(t, "stop", chunks[len(chunks)-1].Get("choices.0.finish_reason").String(),
		"a turn with no surviving tool call must not claim finish_reason tool_calls")
}

// An upstream failure before any output must be returned to dispatch as an
// UpstreamErrorResponse so the proxy can still fall back to chat/completions.
func TestResponsesToOpenAIChatWriter_PreOutputFailureIsRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"upstream_error","message":"Upstream call failed."}}}

`))
	var upstream *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &upstream, "a pre-output failure must stay retryable")
	assert.Contains(t, string(upstream.Body), "Upstream call failed.")
	assert.NotContains(t, rec.Body.String(), "data: [DONE]", "nothing terminal may be committed on a retryable failure")
}

// Once output is committed the response can't be retried, so the failure must
// be rendered in-stream and the stream closed properly.
func TestResponsesToOpenAIChatWriter_PostOutputFailureIsInStream(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(`data: {"type":"response.output_text.delta","output_index":0,"delta":"partial"}

data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"upstream_error","message":"boom"}}}

`))
	require.NoError(t, err, "a committed response must not be reported as retryable")

	chunks := chatChunks(t, rec.Body.String())
	assert.Equal(t, "partial", concatDelta(chunks, "content"))
	assert.Equal(t, "boom", chunks[len(chunks)-1].Get("error.message").String())
}

// A stream that dies without a terminal event must surface an error to the
// proxy (usage accounting depends on it) and still close the client's stream.
func TestResponsesToOpenAIChatWriter_TruncatedStream(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(`data: {"type":"response.output_text.delta","output_index":0,"delta":"half"}

`))
	require.NoError(t, err)
	require.Error(t, w.Finalize(), "an unterminated stream is an error, not a clean stop")
	assert.Contains(t, rec.Body.String(), "data: [DONE]")
	assert.Contains(t, rec.Body.String(), "before a terminal event")
}

// A frame the translator can't parse means content was lost, so it must never
// be skipped into an apparently clean completion. Pre-output it stays
// retryable, exactly like an upstream failure event.
func TestResponsesToOpenAIChatWriter_MalformedFrameIsRetryablePreOutput(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte("data: {\"type\":\"response.output_text.del\n\n"))

	var upstream *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &upstream, "a corrupt frame before output must stay retryable")
	assert.Contains(t, string(upstream.Body), "malformed")
	assert.NotContains(t, rec.Body.String(), "data: [DONE]")
}

// Post-output the same corruption can't be retried, so the client must see an
// error frame instead of a finish_reason that claims the turn completed.
func TestResponsesToOpenAIChatWriter_MalformedFrameIsReportedPostOutput(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(`data: {"type":"response.output_text.delta","output_index":0,"delta":"partial"}

data: {"type":"response.outp

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}

`))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	chunks := chatChunks(t, rec.Body.String())
	assert.Equal(t, "partial", concatDelta(chunks, "content"))
	assert.Contains(t, chunks[len(chunks)-1].Get("error.message").String(), "malformed")
	for _, c := range chunks {
		assert.NotEqual(t, "stop", c.Get("choices.0.finish_reason").String(),
			"a turn that lost a frame must not report a clean stop")
	}
	assert.Contains(t, rec.Body.String(), "data: [DONE]")
}

// A `[DONE]` sentinel is not JSON but is emitted by some gateways, so it must
// not be mistaken for corruption.
func TestResponsesToOpenAIChatWriter_DoneSentinelIsNotMalformed(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(`data: {"type":"response.output_text.delta","output_index":0,"delta":"hi"}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}

data: [DONE]

`))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	chunks := chatChunks(t, rec.Body.String())
	assert.Equal(t, "hi", concatDelta(chunks, "content"))
	assert.Equal(t, "stop", chunks[len(chunks)-1].Get("choices.0.finish_reason").String())
}

// A non-streaming client whose upstream failed gets a chat-shape error body
// rather than a 200 with an empty completion.
func TestResponsesToOpenAIChatWriter_NonStreamingFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)
	require.NoError(t, w.Prelude(false))
	_, err := w.Write([]byte(`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"upstream_error","message":"Upstream call failed."}}}

`))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	assert.Equal(t, 502, rec.Code)
	root := gjson.ParseBytes(rec.Body.Bytes())
	assert.Equal(t, "Upstream call failed.", root.Get("error.message").String())
	assert.Equal(t, "upstream_error", root.Get("error.type").String())
}

// The output-progress watchdog must not be fed by reasoning-only frames, or a
// stream that reasons forever without answering looks healthy.
func TestResponsesToOpenAIChatWriter_ReasoningDoesNotMarkOutputProgress(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.6-luna", nil)
	require.NoError(t, w.Prelude(true))
	marks := 0
	require.True(t, w.ArmOutputProgress(func() { marks++ }))

	_, err := w.Write([]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","summary":[]}}

data: {"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"thinking"}

`))
	require.NoError(t, err)
	assert.Zero(t, marks, "reasoning is not output")

	_, err = w.Write([]byte(`data: {"type":"response.output_text.delta","output_index":1,"delta":"answer"}

`))
	require.NoError(t, err)
	assert.Positive(t, marks)
}
