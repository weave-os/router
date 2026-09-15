package translate_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/translate"
)

// Every sink-owning translator must report an explicit upstream output cap
// through UsageSink.RecordOutputLimitReached, and only that: a healthy
// tool_use / end_turn / stop terminal never sets it however many output
// tokens it carries, a stream cut without a terminal never sets it, and a
// raw cap survives the tool repair / stop-reason promotion the client sees.
// The client-facing wire must be unchanged by the observation.

const (
	outputLimitAnthropicModel = "claude-output-limit-fixture"
	outputLimitChatModel      = "chat-output-limit-fixture"
	outputLimitGeminiModel    = "gemini-output-limit-fixture"
	outputLimitResponsesModel = "responses-output-limit-fixture"

	// healthyLongOutputTokens sits above the retired 8k count heuristic so
	// these cases prove token volume alone does not imply a cap.
	healthyLongOutputTokens = 12000
	cappedOutputTokens      = 64000
)

// headerDrivenTranslator is the setup shape shared by SSETranslator,
// AnthropicSSETranslator, and GeminiToOpenAISSETranslator: streaming mode is
// committed by the upstream Content-Type at WriteHeader.
type headerDrivenTranslator interface {
	http.ResponseWriter
	Finalize() error
}

func driveSSE(t *testing.T, w headerDrivenTranslator, events ...string) error {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, e := range events {
		_, err := w.Write([]byte(e))
		require.NoError(t, err)
	}
	return w.Finalize()
}

func driveJSON(t *testing.T, w headerDrivenTranslator, body string) error {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(body))
	require.NoError(t, err)
	return w.Finalize()
}

// preludeDrivenWriter is the setup shape shared by both Responses writers:
// Prelude picks the client mode, and the upstream is always a Responses SSE
// stream whose terminal event carries the final response object.
type preludeDrivenWriter interface {
	Prelude(streaming bool) error
	Write([]byte) (int, error)
	Finalize() error
}

func driveResponses(t *testing.T, w preludeDrivenWriter, streaming bool, stream string) error {
	t.Helper()
	require.NoError(t, w.Prelude(streaming))
	if _, err := w.Write([]byte(stream)); err != nil {
		return err
	}
	return w.Finalize()
}

// --- Anthropic Messages -> OpenAI chat (SSETranslator) ---

func anthropicMessagesStream(stopReason string, outputTokens int, toolUse bool) []string {
	events := []string{
		"event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"` + outputLimitAnthropicModel + `","content":[],"stop_reason":null,"usage":{"input_tokens":150,"output_tokens":0}}}` + "\n\n",
	}
	if toolUse {
		events = append(events,
			"event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Edit","input":{}}}`+"\n\n",
			"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.go\"}"}}`+"\n\n",
		)
	} else {
		events = append(events,
			"event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n",
			"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`+"\n\n",
		)
	}
	return append(events,
		"event: content_block_stop\ndata: "+`{"type":"content_block_stop","index":0}`+"\n\n",
		fmt.Sprintf("event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":%d}}`+"\n\n", stopReason, outputTokens),
		"event: message_stop\ndata: "+`{"type":"message_stop"}`+"\n\n",
	)
}

func anthropicMessagesBody(stopReason string, outputTokens int, toolUse bool) string {
	content := `[{"type":"text","text":"partial"}]`
	if toolUse {
		content = `[{"type":"tool_use","id":"toolu_1","name":"Edit","input":{"path":"a.go"}}]`
	}
	return fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":%s,"stop_reason":%q,"stop_sequence":null,"usage":{"input_tokens":150,"output_tokens":%d}}`,
		outputLimitAnthropicModel, content, stopReason, outputTokens)
}

func TestSSETranslator_OutputLimit(t *testing.T) {
	cases := []struct {
		name             string
		stopReason       string
		outputTokens     int
		toolUse          bool
		wantReached      bool
		wantFinishReason string
	}{
		{"raw max_tokens is a cap", "max_tokens", cappedOutputTokens, false, true, "length"},
		{"long tool_use handoff is not a cap", "tool_use", healthyLongOutputTokens, true, false, "tool_calls"},
		{"long end_turn is not a cap", "end_turn", cappedOutputTokens / 2, false, false, "stop"},
	}
	for _, tc := range cases {
		t.Run("streaming/"+tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sink := &fakeUsageSink{}
			w := translate.NewSSETranslator(rec, outputLimitAnthropicModel, sink)
			require.NoError(t, driveSSE(t, w, anthropicMessagesStream(tc.stopReason, tc.outputTokens, tc.toolUse)...))

			assert.Equal(t, tc.wantReached, sink.outputLimitReached)
			assert.Equal(t, tc.outputTokens, sink.output, "usage recording is unchanged")
			body := rec.Body.String()
			assert.Contains(t, body, fmt.Sprintf(`"finish_reason":%q`, tc.wantFinishReason))
			assert.Contains(t, body, "data: [DONE]")
		})
		t.Run("buffered/"+tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sink := &fakeUsageSink{}
			w := translate.NewSSETranslator(rec, outputLimitAnthropicModel, sink)
			require.NoError(t, driveJSON(t, w, anthropicMessagesBody(tc.stopReason, tc.outputTokens, tc.toolUse)))

			assert.Equal(t, tc.wantReached, sink.outputLimitReached)
			assert.Equal(t, tc.outputTokens, sink.output)
			root := gjson.ParseBytes(rec.Body.Bytes())
			assert.Equal(t, tc.wantFinishReason, root.Get("choices.0.finish_reason").String())
			assert.EqualValues(t, tc.outputTokens, root.Get("usage.completion_tokens").Int())
			if tc.toolUse {
				assert.Equal(t, "Edit", root.Get("choices.0.message.tool_calls.0.function.name").String())
			}
		})
	}
}

func TestSSETranslator_OutputLimit_NoTerminalIsNotACap(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	w := translate.NewSSETranslator(rec, outputLimitAnthropicModel, sink)
	events := anthropicMessagesStream("max_tokens", cappedOutputTokens, false)
	// Cut before message_delta: no terminal statement ever arrives.
	require.ErrorIs(t, driveSSE(t, w, events[:3]...), translate.ErrStreamIncomplete)

	assert.False(t, sink.outputLimitReached, "a cut stream states no cap")
	assert.NotContains(t, rec.Body.String(), "data: [DONE]")
}

// --- OpenAI chat -> Anthropic Messages (AnthropicSSETranslator) ---

func chatTextFrame(text string) string {
	return fmt.Sprintf(`data: {"id":"c1","object":"chat.completion.chunk","model":%q,"choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`+"\n\n", outputLimitChatModel, text)
}

// chatToolCallFrame opens tool call 0 with the given raw arguments text; a
// truncated JSON fragment models a call the cap cut mid-arguments.
func chatToolCallFrame(args string) string {
	return fmt.Sprintf(`data: {"id":"c1","object":"chat.completion.chunk","model":%q,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Edit","arguments":%q}}]},"finish_reason":null}]}`+"\n\n", outputLimitChatModel, args)
}

func chatFinishFrame(finishReason string, completionTokens int) string {
	return fmt.Sprintf(`data: {"id":"c1","object":"chat.completion.chunk","model":%q,"choices":[{"index":0,"delta":{},"finish_reason":%q}],"usage":{"prompt_tokens":40,"completion_tokens":%d}}`+"\n\n", outputLimitChatModel, finishReason, completionTokens)
}

const chatDoneFrame = "data: [DONE]\n\n"

const truncatedEditArgs = `{"path":"a.go","old_string":"hel`

func TestAnthropicSSETranslator_OutputLimit_Streaming(t *testing.T) {
	cases := []struct {
		name           string
		events         []string
		wantReached    bool
		wantStopReason string
		wantToolUse    bool
	}{
		{
			name:           "raw length on text is a cap",
			events:         []string{chatTextFrame("partial"), chatFinishFrame("length", cappedOutputTokens), chatDoneFrame},
			wantReached:    true,
			wantStopReason: "max_tokens",
		},
		{
			name:           "raw length survives tool repair and tool_use promotion",
			events:         []string{chatToolCallFrame(truncatedEditArgs), chatFinishFrame("length", cappedOutputTokens), chatDoneFrame},
			wantReached:    true,
			wantStopReason: "tool_use",
			wantToolUse:    true,
		},
		{
			name:           "long tool_calls handoff is not a cap",
			events:         []string{chatToolCallFrame(`{"path":"a.go"}`), chatFinishFrame("tool_calls", healthyLongOutputTokens), chatDoneFrame},
			wantReached:    false,
			wantStopReason: "tool_use",
			wantToolUse:    true,
		},
		{
			name:           "long stop is not a cap",
			events:         []string{chatTextFrame("done"), chatFinishFrame("stop", cappedOutputTokens/2), chatDoneFrame},
			wantReached:    false,
			wantStopReason: "end_turn",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sink := &fakeUsageSink{}
			w := translate.NewAnthropicSSETranslator(rec, outputLimitChatModel, sink)
			require.NoError(t, driveSSE(t, w, tc.events...))

			assert.Equal(t, tc.wantReached, sink.outputLimitReached)
			body := rec.Body.String()
			assert.Contains(t, body, fmt.Sprintf(`"stop_reason":%q`, tc.wantStopReason))
			assert.Equal(t, tc.wantStopReason, w.Summary().StopReason, "emitted stop_reason is unchanged")
			assert.Contains(t, body, "event: message_stop")
			if tc.wantToolUse {
				assert.Contains(t, body, `"type":"tool_use"`)
				assert.Contains(t, body, `"name":"Edit"`)
			}
		})
	}
}

// The repaired tool call still reaches the client intact while the raw cap
// is reported: the observation must not alter the repair.
func TestAnthropicSSETranslator_OutputLimit_RepairedToolArgsUnchanged(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	w := translate.NewAnthropicSSETranslator(rec, outputLimitChatModel, sink)
	require.NoError(t, driveSSE(t, w, chatToolCallFrame(truncatedEditArgs), chatFinishFrame("length", cappedOutputTokens), chatDoneFrame))

	assert.True(t, sink.outputLimitReached)
	assert.Contains(t, rec.Body.String(), `"partial_json":"{\"path\":\"a.go\",\"old_string\":\"hel\"}"`,
		"repair closes the truncated args exactly as before")
	summary := w.Summary()
	assert.Equal(t, "length", summary.UpstreamFinishReason)
	assert.True(t, summary.StopReasonPromoted)
	assert.Equal(t, 1, summary.ToolUseBlocks)
}

func TestAnthropicSSETranslator_OutputLimit_NoTerminalIsNotACap(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	w := translate.NewAnthropicSSETranslator(rec, outputLimitChatModel, sink)
	// A usage-only frame with a large completion count, then EOF with no
	// finish_reason: volume is not evidence.
	usageOnly := fmt.Sprintf(`data: {"id":"c1","object":"chat.completion.chunk","model":%q,"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":%d}}`+"\n\n", outputLimitChatModel, cappedOutputTokens)
	require.ErrorIs(t, driveSSE(t, w, chatTextFrame("partial"), usageOnly), translate.ErrStreamIncomplete)

	assert.Equal(t, cappedOutputTokens, sink.output, "usage still flows to the sink")
	assert.False(t, sink.outputLimitReached)
	assert.NotContains(t, rec.Body.String(), "event: message_stop")
}

func chatCompletionBody(finishReason string, completionTokens int, toolCall bool) string {
	message := `{"role":"assistant","content":"partial"}`
	if toolCall {
		message = `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"Edit","arguments":"{\"path\":\"a.go\"}"}}]}`
	}
	return fmt.Sprintf(`{"id":"c1","object":"chat.completion","model":%q,"choices":[{"index":0,"message":%s,"finish_reason":%q}],"usage":{"prompt_tokens":40,"completion_tokens":%d}}`,
		outputLimitChatModel, message, finishReason, completionTokens)
}

func TestAnthropicSSETranslator_OutputLimit_Buffered(t *testing.T) {
	cases := []struct {
		name           string
		finishReason   string
		tokens         int
		toolCall       bool
		wantReached    bool
		wantStopReason string
	}{
		{"raw length on text is a cap", "length", cappedOutputTokens, false, true, "max_tokens"},
		{"raw length with a tool call is a cap despite promotion", "length", cappedOutputTokens, true, true, "tool_use"},
		{"long tool_calls is not a cap", "tool_calls", healthyLongOutputTokens, true, false, "tool_use"},
		{"long stop is not a cap", "stop", cappedOutputTokens / 2, false, false, "end_turn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sink := &fakeUsageSink{}
			w := translate.NewAnthropicSSETranslator(rec, outputLimitChatModel, sink)
			require.NoError(t, driveJSON(t, w, chatCompletionBody(tc.finishReason, tc.tokens, tc.toolCall)))

			assert.Equal(t, tc.wantReached, sink.outputLimitReached)
			assert.Equal(t, tc.tokens, sink.output)
			root := gjson.ParseBytes(rec.Body.Bytes())
			assert.Equal(t, tc.wantStopReason, root.Get("stop_reason").String())
			if tc.toolCall {
				assert.Equal(t, "tool_use", root.Get("content.0.type").String())
			}
		})
	}
}

// --- Gemini -> OpenAI chat (GeminiToOpenAISSETranslator) ---

func geminiStream(finishReason string, outputTokens int, functionCall bool) []string {
	part := `{"text":"partial"}`
	if functionCall {
		part = `{"functionCall":{"name":"get_weather","args":{"location":"NYC"}}}`
	}
	return []string{
		`data: {"candidates":[{"content":{"parts":[` + part + `]}}]}` + "\n\n",
		fmt.Sprintf(`data: {"candidates":[{"content":{"parts":[]},"finishReason":%q}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":%d,"totalTokenCount":%d}}`+"\n\n", finishReason, outputTokens, 100+outputTokens),
	}
}

func geminiCandidateBody(finishReason string, outputTokens int, functionCall bool) string {
	part := `{"text":"partial"}`
	if functionCall {
		part = `{"functionCall":{"name":"get_weather","args":{"location":"NYC"}}}`
	}
	return fmt.Sprintf(`{"candidates":[{"content":{"parts":[%s]},"finishReason":%q}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":%d,"totalTokenCount":%d}}`,
		part, finishReason, outputTokens, 100+outputTokens)
}

func TestGeminiToOpenAISSETranslator_OutputLimit(t *testing.T) {
	cases := []struct {
		name             string
		finishReason     string
		outputTokens     int
		functionCall     bool
		wantReached      bool
		wantFinishReason string
	}{
		{"raw MAX_TOKENS on text is a cap", "MAX_TOKENS", cappedOutputTokens, false, true, "length"},
		{"raw MAX_TOKENS with a functionCall is a cap", "MAX_TOKENS", cappedOutputTokens, true, true, "length"},
		{"long STOP with a functionCall is not a cap", "STOP", healthyLongOutputTokens, true, false, "tool_calls"},
		{"long STOP is not a cap", "STOP", cappedOutputTokens / 2, false, false, "stop"},
	}
	for _, tc := range cases {
		t.Run("streaming/"+tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sink := &fakeUsageSink{}
			w := translate.NewGeminiToOpenAISSETranslator(rec, outputLimitGeminiModel, sink)
			require.NoError(t, driveSSE(t, w, geminiStream(tc.finishReason, tc.outputTokens, tc.functionCall)...))

			assert.Equal(t, tc.wantReached, sink.outputLimitReached)
			assert.Equal(t, tc.outputTokens, sink.output)
			body := rec.Body.String()
			assert.Contains(t, body, fmt.Sprintf(`"finish_reason":%q`, tc.wantFinishReason))
			assert.Contains(t, body, "data: [DONE]")
			if tc.functionCall {
				assert.Contains(t, body, `"name":"get_weather"`)
			}
		})
		t.Run("buffered/"+tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sink := &fakeUsageSink{}
			w := translate.NewGeminiToOpenAISSETranslator(rec, outputLimitGeminiModel, sink)
			require.NoError(t, driveJSON(t, w, geminiCandidateBody(tc.finishReason, tc.outputTokens, tc.functionCall)))

			assert.Equal(t, tc.wantReached, sink.outputLimitReached)
			assert.Equal(t, tc.outputTokens, sink.output)
			root := gjson.ParseBytes(rec.Body.Bytes())
			assert.Equal(t, tc.wantFinishReason, root.Get("choices.0.finish_reason").String())
		})
	}
}

func TestGeminiToOpenAISSETranslator_OutputLimit_NoTerminalIsNotACap(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	w := translate.NewGeminiToOpenAISSETranslator(rec, outputLimitGeminiModel, sink)
	require.ErrorIs(t, driveSSE(t, w, geminiStream("MAX_TOKENS", cappedOutputTokens, false)[:1]...), translate.ErrStreamIncomplete)

	assert.False(t, sink.outputLimitReached)
	assert.NotContains(t, rec.Body.String(), "data: [DONE]")
}

// Gemini -> chat -> Messages is how the proxy serves an Anthropic client from
// Gemini: the outer translator has no sink and maps MAX_TOKENS to chat
// "length", which the inner translator records before promoting the
// surviving functionCall to stop_reason tool_use.
func TestGeminiToAnthropicChain_OutputLimit(t *testing.T) {
	cases := []struct {
		name           string
		finishReason   string
		outputTokens   int
		wantReached    bool
		wantStopReason string
	}{
		{"raw MAX_TOKENS with a functionCall is a cap", "MAX_TOKENS", cappedOutputTokens, true, "tool_use"},
		{"long STOP with a functionCall is not a cap", "STOP", healthyLongOutputTokens, false, "tool_use"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sink := &fakeUsageSink{}
			anthropicTr := translate.NewAnthropicSSETranslator(rec, outputLimitGeminiModel, sink)
			require.NoError(t, anthropicTr.Prelude(true))
			geminiTr := translate.NewGeminiToOpenAISSETranslator(anthropicTr, outputLimitGeminiModel, nil)
			require.NoError(t, driveSSE(t, geminiTr, geminiStream(tc.finishReason, tc.outputTokens, true)...))
			require.NoError(t, anthropicTr.Finalize())

			assert.Equal(t, tc.wantReached, sink.outputLimitReached)
			assert.Equal(t, tc.outputTokens, sink.output)
			body := rec.Body.String()
			assert.Contains(t, body, `"type":"tool_use"`)
			assert.Contains(t, body, fmt.Sprintf(`"stop_reason":%q`, tc.wantStopReason))
			assert.Equal(t, tc.wantStopReason, anthropicTr.Summary().StopReason)
		})
	}
}

// --- OpenAI Responses -> Anthropic Messages / OpenAI chat (both writers) ---

// responsesStreamPrefix is the shared fixture's reasoning, message, and
// completed function_call items, without its terminal event.
func responsesStreamPrefix() string {
	i := strings.Index(responsesStreamFixture, "event: response.completed")
	if i < 0 {
		panic("responsesStreamFixture lost its terminal event")
	}
	return responsesStreamFixture[:i]
}

// responsesTerminal renders a terminal event whose response object carries the
// shared fixture's completed function_call. status is "completed" or
// "incomplete"; an incomplete status is attributed to max_output_tokens.
func responsesTerminal(status string, outputTokens int) string {
	eventType, details := "response.completed", "null"
	if status == "incomplete" {
		eventType, details = "response.incomplete", `{"reason":"max_output_tokens"}`
	}
	return fmt.Sprintf("event: %s\ndata: "+`{"type":%q,"response":{"id":"resp_abc","status":%q,"model":%q,"incomplete_details":%s,"output":[{"id":"rs_1","type":"reasoning","encrypted_content":"enc_stream","summary":[{"type":"summary_text","text":"Checking the weather tool."}]},{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Let me check the weather."}]},{"id":"fc_1","type":"function_call","call_id":"call_xyz","name":"get_weather","arguments":"{\"location\":\"NYC\"}"}],"usage":{"input_tokens":150,"output_tokens":%d}}}`+"\n\n",
		eventType, eventType, status, outputLimitResponsesModel, details, outputTokens)
}

// responsesTruncatedToolCallStream is a function_call whose arguments the cap
// cut mid-stream: no output_item.done, and the terminal response carries the
// partial arguments.
func responsesTruncatedToolCallStream() string {
	return `event: response.created
data: {"type":"response.created","response":{"id":"resp_cut","status":"in_progress","model":"` + outputLimitResponsesModel + `","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"Edit","arguments":"","status":"in_progress"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"path\":\"a.go\",\"old_string\":\"hel"}

event: response.incomplete
data: {"type":"response.incomplete","response":{"id":"resp_cut","status":"incomplete","model":"` + outputLimitResponsesModel + `","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"Edit","arguments":"{\"path\":\"a.go\",\"old_string\":\"hel","status":"incomplete"}],"usage":{"input_tokens":100,"output_tokens":` + fmt.Sprint(cappedOutputTokens) + `}}}

`
}

// responsesTextOnlyCapStream is a plain answer the cap cut off.
func responsesTextOnlyCapStream() string {
	return `event: response.created
data: {"type":"response.created","response":{"id":"resp_txt","status":"in_progress","model":"` + outputLimitResponsesModel + `","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"partial"}

event: response.incomplete
data: {"type":"response.incomplete","response":{"id":"resp_txt","status":"incomplete","model":"` + outputLimitResponsesModel + `","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}],"usage":{"input_tokens":100,"output_tokens":` + fmt.Sprint(cappedOutputTokens) + `}}}

`
}

// responsesNoTerminalStream ends after output with no terminal event.
func responsesNoTerminalStream() string {
	s := responsesTextOnlyCapStream()
	return s[:strings.Index(s, "event: response.incomplete")]
}

type responsesOutputLimitCase struct {
	name        string
	stream      string
	wantReached bool
	// wantAnthropicStop / wantChatFinish are the unchanged client terminals.
	wantAnthropicStop string
	wantChatFinish    string
	wantToolCall      bool
	wantToolName      string
	wantChatStreamErr error
}

func responsesOutputLimitCases() []responsesOutputLimitCase {
	return []responsesOutputLimitCase{
		{
			name:              "long completed tool call is not a cap",
			stream:            responsesStreamPrefix() + responsesTerminal("completed", healthyLongOutputTokens),
			wantReached:       false,
			wantAnthropicStop: "tool_use",
			wantChatFinish:    "tool_calls",
			wantToolCall:      true,
			wantToolName:      "get_weather",
		},
		{
			name:              "raw incomplete max_output_tokens with a completed tool call is a cap",
			stream:            responsesStreamPrefix() + responsesTerminal("incomplete", cappedOutputTokens),
			wantReached:       true,
			wantAnthropicStop: "tool_use",
			wantChatFinish:    "tool_calls",
			wantToolCall:      true,
			wantToolName:      "get_weather",
		},
		{
			name:              "raw incomplete max_output_tokens with a truncated tool call is a cap",
			stream:            responsesTruncatedToolCallStream(),
			wantChatStreamErr: translate.ErrStreamOrder,
			wantReached:       true,
			wantAnthropicStop: "tool_use",
			wantChatFinish:    "tool_calls",
			wantToolCall:      true,
			wantToolName:      "Edit",
		},
		{
			name:              "raw incomplete max_output_tokens on text is a cap",
			stream:            responsesTextOnlyCapStream(),
			wantReached:       true,
			wantAnthropicStop: "max_tokens",
			wantChatFinish:    "length",
		},
	}
}

func TestResponsesToAnthropicWriter_OutputLimit(t *testing.T) {
	for _, tc := range responsesOutputLimitCases() {
		t.Run("streaming/"+tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sink := &fakeUsageSink{}
			w := translate.NewResponsesToAnthropicWriter(rec, outputLimitResponsesModel, sink)
			require.NoError(t, driveResponses(t, w, true, tc.stream))

			assert.Equal(t, tc.wantReached, sink.outputLimitReached)
			body := rec.Body.String()
			assert.Contains(t, body, fmt.Sprintf(`"stop_reason":%q`, tc.wantAnthropicStop))
			assert.Equal(t, tc.wantAnthropicStop, w.Summary().StopReason)
			assert.Contains(t, body, "event: message_stop")
			if tc.wantToolCall {
				assert.Contains(t, body, `"type":"tool_use"`)
				assert.Contains(t, body, fmt.Sprintf(`"name":%q`, tc.wantToolName))
			}
		})
		t.Run("buffered/"+tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sink := &fakeUsageSink{}
			w := translate.NewResponsesToAnthropicWriter(rec, outputLimitResponsesModel, sink)
			require.NoError(t, driveResponses(t, w, false, tc.stream))

			assert.Equal(t, tc.wantReached, sink.outputLimitReached)
			root := gjson.ParseBytes(rec.Body.Bytes())
			assert.Equal(t, tc.wantAnthropicStop, root.Get("stop_reason").String())
			if tc.wantToolCall {
				assert.Equal(t, tc.wantToolName, root.Get(`content.#(type=="tool_use").name`).String())
			}
		})
	}
}

func TestResponsesToAnthropicWriter_OutputLimit_NoTerminalIsNotACap(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	w := translate.NewResponsesToAnthropicWriter(rec, outputLimitResponsesModel, sink)
	require.ErrorIs(t, driveResponses(t, w, true, responsesNoTerminalStream()), translate.ErrStreamIncomplete)

	assert.False(t, sink.outputLimitReached)
	assert.NotContains(t, rec.Body.String(), "event: message_stop")
}

func TestResponsesToOpenAIChatWriter_OutputLimit(t *testing.T) {
	for _, tc := range responsesOutputLimitCases() {
		t.Run("streaming/"+tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sink := &fakeUsageSink{}
			w := translate.NewResponsesToOpenAIChatWriter(rec, outputLimitResponsesModel, sink)
			err := driveResponses(t, w, true, tc.stream)
			if tc.wantChatStreamErr != nil {
				// The existing writer rejects a pending tool flush after Terminal;
				// keep that error contract while retaining the upstream cap fact.
				require.ErrorIs(t, err, tc.wantChatStreamErr)
				assert.Equal(t, tc.wantReached, sink.outputLimitReached)
				assert.NotContains(t, rec.Body.String(), `"finish_reason":"tool_calls"`)
				return
			}
			require.NoError(t, err)

			assert.Equal(t, tc.wantReached, sink.outputLimitReached)
			chunks := chatChunks(t, rec.Body.String())
			require.NotEmpty(t, chunks)
			assert.Equal(t, tc.wantChatFinish, chunks[len(chunks)-1].Get("choices.0.finish_reason").String())
			assert.Equal(t, tc.wantChatFinish, w.Summary().StopReason)
			if tc.wantToolCall {
				var names []string
				for _, c := range chunks {
					if name := c.Get("choices.0.delta.tool_calls.0.function.name").String(); name != "" {
						names = append(names, name)
					}
				}
				assert.Equal(t, []string{tc.wantToolName}, names)
			}
		})
		t.Run("buffered/"+tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sink := &fakeUsageSink{}
			w := translate.NewResponsesToOpenAIChatWriter(rec, outputLimitResponsesModel, sink)
			require.NoError(t, driveResponses(t, w, false, tc.stream))

			assert.Equal(t, tc.wantReached, sink.outputLimitReached)
			root := gjson.ParseBytes(rec.Body.Bytes())
			assert.Equal(t, tc.wantChatFinish, root.Get("choices.0.finish_reason").String())
			if tc.wantToolCall {
				assert.Equal(t, tc.wantToolName, root.Get("choices.0.message.tool_calls.0.function.name").String())
			}
		})
	}
}

func TestResponsesToOpenAIChatWriter_OutputLimit_NoTerminalIsNotACap(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	w := translate.NewResponsesToOpenAIChatWriter(rec, outputLimitResponsesModel, sink)
	require.ErrorIs(t, driveResponses(t, w, true, responsesNoTerminalStream()), translate.ErrStreamIncomplete)

	assert.False(t, sink.outputLimitReached)
	assert.Contains(t, rec.Body.String(), "before a terminal event",
		"the cut is reported as an error, not a cap")
	assert.NotContains(t, rec.Body.String(), `"finish_reason":"length"`)
}

// --- native Responses, forwarded verbatim (ResponsesTerminal) ---

func TestResponsesTerminal_OutputLimitReached(t *testing.T) {
	cases := []struct {
		name          string
		payload       string
		wantOK        bool
		wantReached   bool
		wantFinish    string
		wantToolCalls int
	}{
		{
			name:          "incomplete max_output_tokens with a function_call keeps the tool finish but states the cap",
			payload:       `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"ls"}],"usage":{"input_tokens":120,"output_tokens":64000}}}`,
			wantOK:        true,
			wantReached:   true,
			wantFinish:    "tool_calls",
			wantToolCalls: 1,
		},
		{
			name:          "long completed function_call is not a cap",
			payload:       `{"type":"response.completed","response":{"id":"resp_1","status":"completed","incomplete_details":null,"output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"ls\"}"}],"usage":{"input_tokens":120,"output_tokens":12000}}}`,
			wantOK:        true,
			wantReached:   false,
			wantFinish:    "tool_calls",
			wantToolCalls: 1,
		},
		{
			name:        "non-streaming incomplete max_output_tokens body is a cap",
			payload:     `{"id":"resp_1","object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}]}`,
			wantOK:      true,
			wantReached: true,
			wantFinish:  "length",
		},
		{
			name:        "long completed answer is not a cap",
			payload:     `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":120,"output_tokens":32000}}}`,
			wantOK:      true,
			wantReached: false,
			wantFinish:  "stop",
		},
		{
			name:    "incomplete for another reason states neither outcome nor cap",
			payload: `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`,
		},
		{
			name:    "in-progress snapshot states nothing",
			payload: `{"id":"resp_1","object":"response","status":"in_progress","incomplete_details":null,"output":[]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := translate.ResponsesTerminal([]byte(tc.payload))
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantReached, got.OutputLimitReached)
			assert.Equal(t, tc.wantFinish, got.FinishReason)
			assert.Equal(t, tc.wantToolCalls, got.ToolCalls)
		})
	}
}
