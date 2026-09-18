package translate_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// These tests pin that where a provider's writes happen to fall never changes
// what a client observes: the same upstream bytes delivered as one write, at
// every two-way split, one byte at a time, in odd-sized pieces, or in
// provider-sized 4 KiB chunks must produce the same wire bytes, errors,
// output-progress marks, usage records, and summaries. Only values a writer
// generates per instance (random ids, wall-clock stamps) are projected away
// before comparison — see projectGeneratedValues.

const (
	// everySplitMaxBytes bounds the fixtures that get the O(n) two-way split
	// enumeration; longer fixtures use fixed-size chunkings instead.
	everySplitMaxBytes = 4096
	// largeFrameBytes is the payload of the single oversized frame every large
	// fixture opens with, several times a provider write.
	largeFrameBytes = 24 << 10
	// largeFixtureSmallFrames is how many ordinary frames follow it.
	largeFixtureSmallFrames = 200

	testMarker = "✦ **Weave Router** → routed\n\n"
)

// streamObservation is everything a caller can see from one writer run.
type streamObservation struct {
	// bodyAfterWrites is what reached the recorder by the time the last Write
	// returned; body is the same after Finalize (identical for writers
	// without one).
	bodyAfterWrites string
	body            string
	statusCode      int
	contentType     string
	writeErr        error
	finalizeErr     error
	progressMarks   int
	usage           fakeUsageSink
	summary         translate.ResponseSummary
}

// comparableObservation is a streamObservation with generated values
// projected away and errors reduced to text, so two runs compare with one
// equality check.
type comparableObservation struct {
	bodyAfterWrites string
	body            string
	statusCode      int
	contentType     string
	writeErr        string
	finalizeErr     string
	progressMarks   int
	usage           fakeUsageSink
	summary         translate.ResponseSummary
}

func (o streamObservation) comparable(t *testing.T) comparableObservation {
	t.Helper()
	return comparableObservation{
		bodyAfterWrites: projectGeneratedValues(t, o.bodyAfterWrites),
		body:            projectGeneratedValues(t, o.body),
		statusCode:      o.statusCode,
		contentType:     o.contentType,
		writeErr:        errorText(o.writeErr),
		finalizeErr:     errorText(o.finalizeErr),
		progressMarks:   o.progressMarks,
		usage:           o.usage,
		summary:         o.summary,
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// streamFixture is one upstream body plus the fixed expectations that prove
// the fixture really exercises the paths it is meant to cover.
type streamFixture struct {
	name string
	body string
	// streaming selects the SSE path; false is a non-streaming client whose
	// body is buffered and rendered once at Finalize.
	streaming bool
	// mustContain / mustNotContain are checked against the one-write
	// reference output.
	mustContain    []string
	mustNotContain []string
	// wantFinalizeErr is the owner's EOF policy for this body; nil means a
	// clean Finalize.
	wantFinalizeErr error
	// wantProgress requires at least one output-progress mark in the reference.
	wantProgress bool
}

func (f streamFixture) checkReference(t *testing.T, reference streamObservation) {
	t.Helper()
	require.NoError(t, reference.writeErr)
	if f.wantFinalizeErr == nil {
		require.NoError(t, reference.finalizeErr)
	} else {
		require.ErrorIs(t, reference.finalizeErr, f.wantFinalizeErr)
	}
	for _, want := range f.mustContain {
		require.Contains(t, reference.body, want)
	}
	for _, unwanted := range f.mustNotContain {
		require.NotContains(t, reference.body, unwanted)
	}
	if f.wantProgress {
		require.Positive(t, reference.progressMarks)
	}
}

// streamFamily is one public writer type with its fixtures and a runner that
// constructs it the way the proxy does and feeds it the given writes.
type streamFamily struct {
	name     string
	fixtures []streamFixture
	run      func(t *testing.T, fixture streamFixture, chunks [][]byte) streamObservation
}

type finalizingWriter interface {
	http.ResponseWriter
	Finalize() error
}

type preludeWriter interface {
	http.ResponseWriter
	Prelude(streaming bool) error
}

type outputProgressArmer interface {
	ArmOutputProgress(mark func()) (armed bool)
}

// commitUpstreamResponse mirrors the proxy handing the upstream's headers to
// a WriteHeader-driven writer.
func commitUpstreamResponse(w http.ResponseWriter, streaming bool) {
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(http.StatusOK)
}

// preludeOrCommit mirrors the proxy driving a Prelude-based writer: a
// streaming client gets the eager prelude; a non-streaming client only sees
// the upstream status, and the writer renders one body at Finalize.
func preludeOrCommit(t *testing.T, w preludeWriter, streaming bool) {
	t.Helper()
	if streaming {
		require.NoError(t, w.Prelude(true))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func armProgress(t *testing.T, w outputProgressArmer, streaming bool) *int {
	t.Helper()
	marks := 0
	armed := w.ArmOutputProgress(func() { marks++ })
	require.Equal(t, streaming, armed, "ArmOutputProgress must report armed exactly for streaming clients")
	return &marks
}

// writeChunks feeds chunks as consecutive Write calls, stopping at the first
// error the way the proxy does.
func writeChunks(w http.ResponseWriter, chunks [][]byte) error {
	for _, chunk := range chunks {
		_, err := w.Write(chunk)
		if err != nil {
			return err
		}
	}
	return nil
}

func runFinalizing(w finalizingWriter, rec *httptest.ResponseRecorder, chunks [][]byte) streamObservation {
	observation := streamObservation{writeErr: writeChunks(w, chunks)}
	observation.bodyAfterWrites = rec.Body.String()
	if observation.writeErr == nil {
		observation.finalizeErr = w.Finalize()
	}
	observation.body = rec.Body.String()
	observation.statusCode = rec.Code
	observation.contentType = rec.Header().Get("Content-Type")
	return observation
}

func runPassthrough(w http.ResponseWriter, rec *httptest.ResponseRecorder, chunks [][]byte) streamObservation {
	observation := streamObservation{writeErr: writeChunks(w, chunks)}
	observation.bodyAfterWrites = rec.Body.String()
	observation.body = observation.bodyAfterWrites
	observation.statusCode = rec.Code
	observation.contentType = rec.Header().Get("Content-Type")
	return observation
}

// --- fixtures ---

// anthropicToolTurnStream is a text block followed by a tool_use block whose
// input arrives in two input_json_delta fragments, ending in stop_reason
// tool_use with usage on both message_start and message_delta.
func anthropicToolTurnStream() string {
	return anthropicToolTurnFrames().join()
}

func anthropicToolTurnFrames() sseFrames {
	return sseFrames{
		buildAnthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_frag_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":0,"cache_creation_input_tokens":3,"cache_read_input_tokens":4}}}`),
		buildAnthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		buildAnthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`),
		buildAnthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", world"}}`),
		buildAnthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`),
		buildAnthropicSSE("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_frag_01","name":"get_weather","input":{}}}`),
		buildAnthropicSSE("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"loc"}}`),
		buildAnthropicSSE("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"ation\":\"NYC\"}"}}`),
		buildAnthropicSSE("content_block_stop", `{"type":"content_block_stop","index":1}`),
		buildAnthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":21}}`),
		buildAnthropicSSE("message_stop", `{"type":"message_stop"}`),
	}
}

const anthropicMessageJSON = `{"id":"msg_json_1","type":"message","role":"assistant","model":"claude-opus-4","content":[{"type":"text","text":"Hi"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":2}}`

// openAIChunkToolTurnFrames streams reasoning, text, and one tool call whose
// arguments arrive in two fragments, then finish_reason tool_calls with
// usage and [DONE].
func openAIChunkToolTurnFrames() sseFrames {
	return sseFrames{
		openAIChunk(`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]`),
		openAIChunk(`"choices":[{"index":0,"delta":{"reasoning_content":"Think first."},"finish_reason":null}]`),
		openAIChunk(`"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]`),
		openAIChunk(`"choices":[{"index":0,"delta":{"content":", world"},"finish_reason":null}]`),
		openAIChunk(`"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_frag_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]`),
		openAIChunk(`"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"location\":"}}]},"finish_reason":null}]`),
		openAIChunk(`"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"NYC\"}"}}]},"finish_reason":null}]`),
		openAIChunk(`"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":40,"completion_tokens":17,"prompt_tokens_details":{"cached_tokens":8}}`),
		"data: [DONE]\n\n",
	}
}

func openAIChunk(choicesAndUsage string) string {
	return `data: {"id":"chatcmpl-frag","object":"chat.completion.chunk","created":1,"model":"gpt-x",` + choicesAndUsage + "}\n\n"
}

const openAIChatCompletionJSON = `{"id":"chatcmpl-json","object":"chat.completion","created":1,"model":"gpt-x","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`

// geminiToolTurnFrames streams text, then a functionCall carrying a
// thoughtSignature, then STOP with usage.
func geminiToolTurnFrames() sseFrames {
	return sseFrames{
		string(geminiSSE(`{"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"index":0}]}`)),
		string(geminiSSE(`{"candidates":[{"content":{"parts":[{"text":", world"}],"role":"model"},"index":0}]}`)),
		string(geminiSSE(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"location":"NYC"}},"thoughtSignature":"SIG_FRAG"}],"role":"model"},"index":0}]}`)),
		string(geminiSSE(`{"candidates":[{"content":{"parts":[],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":11,"totalTokenCount":41}}`)),
	}
}

const geminiResponseJSON = `{"candidates":[{"content":{"parts":[{"text":"Hi"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`

// sseFrames is an ordered list of complete SSE events; truncating it models
// an upstream that closes before its terminal event.
type sseFrames []string

func (f sseFrames) join() string {
	return strings.Join(f, "")
}

// withoutLast drops the final n frames.
func (f sseFrames) withoutLast(n int) sseFrames {
	return f[:len(f)-n]
}

func largeText(n int) string {
	return strings.Repeat("x", n)
}

// largeAnthropicStreamOf is a text-only Anthropic turn: one text_delta of
// frameBytes, then smallFrames ordinary deltas, ending naturally so footer
// injection has a target.
func largeAnthropicStreamOf(frameBytes, smallFrames int) string {
	var body strings.Builder
	body.WriteString(buildAnthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_large","type":"message","role":"assistant","content":[],"model":"claude-opus-4","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":0}}}`))
	body.WriteString(buildAnthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	body.WriteString(buildAnthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+largeText(frameBytes)+`"}}`))
	for i := 0; i < smallFrames; i++ {
		body.WriteString(buildAnthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"chunk `+strconv.Itoa(i)+` `+largeText(90)+`"}}`))
	}
	body.WriteString(buildAnthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`))
	body.WriteString(buildAnthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4321}}`))
	body.WriteString(buildAnthropicSSE("message_stop", `{"type":"message_stop"}`))
	return body.String()
}

func largeAnthropicStream() string {
	return largeAnthropicStreamOf(largeFrameBytes, largeFixtureSmallFrames)
}

// largeOpenAIChunkStreamOf is a text-only chat.completion.chunk turn ending
// in finish_reason stop, usage, and [DONE].
func largeOpenAIChunkStreamOf(frameBytes, smallFrames int) string {
	var body strings.Builder
	body.WriteString(openAIChunk(`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]`))
	body.WriteString(openAIChunk(`"choices":[{"index":0,"delta":{"content":"` + largeText(frameBytes) + `"},"finish_reason":null}]`))
	for i := 0; i < smallFrames; i++ {
		body.WriteString(openAIChunk(`"choices":[{"index":0,"delta":{"content":"chunk ` + strconv.Itoa(i) + ` ` + largeText(90) + `"},"finish_reason":null}]`))
	}
	body.WriteString(openAIChunk(`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":4321}`))
	body.WriteString("data: [DONE]\n\n")
	return body.String()
}

func largeOpenAIChunkStream() string {
	return largeOpenAIChunkStreamOf(largeFrameBytes, largeFixtureSmallFrames)
}

// largeGeminiStream is a text-only Gemini turn ending in STOP with usage.
func largeGeminiStream() string {
	var body strings.Builder
	body.Write(geminiSSE(`{"candidates":[{"content":{"parts":[{"text":"` + largeText(largeFrameBytes) + `"}],"role":"model"},"index":0}]}`))
	for i := 0; i < largeFixtureSmallFrames; i++ {
		body.Write(geminiSSE(`{"candidates":[{"content":{"parts":[{"text":"chunk ` + strconv.Itoa(i) + ` ` + largeText(90) + `"}],"role":"model"},"index":0}]}`))
	}
	body.Write(geminiSSE(`{"candidates":[{"content":{"parts":[],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":4321,"totalTokenCount":4351}}`))
	return body.String()
}

func responsesEvent(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

// largeResponsesStream is a message with one oversized text delta plus many
// small ones, then a function_call whose arguments stream in ~100-byte
// deltas, then response.completed.
func largeResponsesStream() string {
	var body strings.Builder
	body.WriteString(responsesEvent("response.created", `{"type":"response.created","response":{"id":"resp_large","status":"in_progress","model":"gpt-5.5","output":[]}}`))
	body.WriteString(responsesEvent("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_large","type":"message","role":"assistant","status":"in_progress","content":[]}}`))
	body.WriteString(responsesEvent("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_large","output_index":0,"content_index":0,"delta":"`+largeText(largeFrameBytes)+`"}`))
	for i := 0; i < largeFixtureSmallFrames; i++ {
		body.WriteString(responsesEvent("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_large","output_index":0,"content_index":0,"delta":"chunk `+strconv.Itoa(i)+` `+largeText(90)+`"}`))
	}
	body.WriteString(responsesEvent("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_large","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"streamed"}]}}`))
	body.WriteString(responsesEvent("response.output_item.added", `{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_large","type":"function_call","call_id":"call_large","name":"write_file","arguments":"","status":"in_progress"}}`))
	// The arguments are plain ASCII, for which strconv.Quote is a valid JSON
	// string encoding.
	arguments := `{"path":"notes.txt","content":"` + strings.Repeat("a", 20<<10) + `"}`
	const deltaBytes = 97
	for start := 0; start < len(arguments); start += deltaBytes {
		piece := arguments[start:min(start+deltaBytes, len(arguments))]
		body.WriteString(responsesEvent("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"fc_large","output_index":1,"delta":`+strconv.Quote(piece)+`}`))
	}
	body.WriteString(responsesEvent("response.output_item.done", `{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_large","type":"function_call","call_id":"call_large","name":"write_file","arguments":`+strconv.Quote(arguments)+`,"status":"completed"}}`))
	body.WriteString(responsesEvent("response.completed", `{"type":"response.completed","response":{"id":"resp_large","status":"completed","model":"gpt-5.5","incomplete_details":null,"output":[],"usage":{"input_tokens":150,"input_tokens_details":{"cached_tokens":0},"output_tokens":4321}}}`))
	return body.String()
}

// withCRLF rewrites LF-only SSE framing as CRLF framing.
func withCRLF(body string) string {
	return strings.ReplaceAll(body, "\n", "\r\n")
}

// --- families ---

func streamFamilies() []streamFamily {
	return []streamFamily{
		{
			name: "SSETranslator",
			fixtures: []streamFixture{
				{
					name: "text and tool_use turn", body: anthropicToolTurnStream(), streaming: true,
					mustContain: []string{`"id":"msg_frag_1"`, `"content":"Hello"`, `"id":"toolu_frag_01"`, `"name":"get_weather"`, `"arguments":"{\"loc"`, `"finish_reason":"tool_calls"`, `"completion_tokens":21`, "data: [DONE]"},
				},
				{
					name: "truncated before message_stop", body: anthropicToolTurnFrames().withoutLast(1).join(), streaming: true,
					mustContain: []string{`"type":"api_error"`}, mustNotContain: []string{"data: [DONE]"}, wantFinalizeErr: translate.ErrStreamIncomplete,
				},
				{
					name: "plain JSON body", body: anthropicMessageJSON,
					mustContain: []string{`"object":"chat.completion"`, `"content":"Hi"`, `"finish_reason":"stop"`},
				},
				{name: "large text turn", body: largeAnthropicStream(), streaming: true, mustContain: []string{`"finish_reason":"stop"`, "data: [DONE]"}},
			},
			run: func(t *testing.T, fixture streamFixture, chunks [][]byte) streamObservation {
				rec := httptest.NewRecorder()
				sink := &fakeUsageSink{}
				w := translate.NewSSETranslator(rec, "claude-opus-4", sink)
				commitUpstreamResponse(w, fixture.streaming)
				observation := runFinalizing(w, rec, chunks)
				observation.usage = *sink
				return observation
			},
		},
		{
			name: "AnthropicSSETranslator",
			fixtures: []streamFixture{
				{
					name: "reasoning, text and tool_calls turn", body: openAIChunkToolTurnFrames().join(), streaming: true,
					mustContain:  []string{`"thinking_delta"`, "Think first.", `"text_delta","text":"Hello"`, `"type":"tool_use","id":"call_frag_1_`, `"name":"get_weather"`, `"partial_json":"{\"location\":\"NYC\"}"`, `"stop_reason":"tool_use"`, `"output_tokens":17`, "event: message_stop"},
					wantProgress: true,
				},
				{
					name: "truncated before finish", body: openAIChunkToolTurnFrames().withoutLast(2).join(), streaming: true,
					mustContain: []string{"event: error"}, mustNotContain: []string{"event: message_stop"}, wantFinalizeErr: translate.ErrStreamIncomplete, wantProgress: true,
				},
				{
					name: "plain JSON body", body: openAIChatCompletionJSON,
					mustContain: []string{`"type":"message"`, `"text":"Hi"`, `"stop_reason":"end_turn"`},
				},
				{name: "large text turn", body: largeOpenAIChunkStream(), streaming: true, mustContain: []string{`"stop_reason":"end_turn"`, "event: message_stop"}, wantProgress: true},
			},
			run: func(t *testing.T, fixture streamFixture, chunks [][]byte) streamObservation {
				rec := httptest.NewRecorder()
				sink := &fakeUsageSink{}
				w := translate.NewAnthropicSSETranslator(rec, "gpt-x", sink)
				preludeOrCommit(t, w, fixture.streaming)
				marks := armProgress(t, w, fixture.streaming)
				observation := runFinalizing(w, rec, chunks)
				observation.progressMarks = *marks
				observation.usage = *sink
				observation.summary = w.Summary()
				return observation
			},
		},
		{
			name: "GeminiToOpenAISSETranslator",
			fixtures: []streamFixture{
				{
					name: "text and functionCall turn", body: geminiToolTurnFrames().join(), streaming: true,
					mustContain:  []string{`"content":"Hello"`, "__thought__U0lHX0ZSQUc", `"name":"get_weather"`, `{\"location\":\"NYC\"}`, `"finish_reason":"tool_calls"`, "data: [DONE]"},
					wantProgress: true,
				},
				{
					name: "truncated before finish", body: geminiToolTurnFrames().withoutLast(1).join(), streaming: true,
					mustContain: []string{`"type":"api_error"`}, mustNotContain: []string{"data: [DONE]"}, wantFinalizeErr: translate.ErrStreamIncomplete, wantProgress: true,
				},
				{
					name: "plain JSON body", body: geminiResponseJSON,
					mustContain: []string{`"object":"chat.completion"`, `"content":"Hi"`, `"finish_reason":"stop"`},
				},
				{name: "large text turn", body: largeGeminiStream(), streaming: true, mustContain: []string{`"finish_reason":"stop"`, "data: [DONE]"}, wantProgress: true},
			},
			run: func(t *testing.T, fixture streamFixture, chunks [][]byte) streamObservation {
				rec := httptest.NewRecorder()
				sink := &fakeUsageSink{}
				w := translate.NewGeminiToOpenAISSETranslator(rec, "gemini-2.5-pro", sink)
				commitUpstreamResponse(w, fixture.streaming)
				marks := armProgress(t, w, fixture.streaming)
				observation := runFinalizing(w, rec, chunks)
				observation.progressMarks = *marks
				observation.usage = *sink
				return observation
			},
		},
		{
			name: "ResponsesToAnthropicWriter",
			fixtures: []streamFixture{
				{
					name: "reasoning, text and function_call turn", body: responsesStreamFixture, streaming: true,
					mustContain:  []string{"call_xyz__openai_reasoning__", `"type":"signature_delta"`, `"partial_json":"{\"location\":\"NYC\"}"`, `"stop_reason":"tool_use"`, "event: message_stop"},
					wantProgress: true,
				},
				{
					name: "truncated before response.completed", body: responsesStreamPrefix(), streaming: true,
					mustContain: []string{"event: error"}, mustNotContain: []string{"event: message_stop"}, wantFinalizeErr: translate.ErrStreamIncomplete, wantProgress: true,
				},
				{
					name: "non-streaming client renders one body", body: responsesStreamFixture,
					mustContain: []string{`"text":"Let me check the weather."`, `"id":"call_xyz`, `"stop_reason":"tool_use"`},
				},
				{name: "large text and tool arguments", body: largeResponsesStream(), streaming: true, mustContain: []string{`"name":"write_file"`, "call_large", `"stop_reason":"tool_use"`, "event: message_stop"}, wantProgress: true},
			},
			run: func(t *testing.T, fixture streamFixture, chunks [][]byte) streamObservation {
				rec := httptest.NewRecorder()
				sink := &fakeUsageSink{}
				w := translate.NewResponsesToAnthropicWriter(rec, "gpt-5.5", sink)
				preludeOrCommit(t, w, fixture.streaming)
				marks := armProgress(t, w, fixture.streaming)
				observation := runFinalizing(w, rec, chunks)
				observation.progressMarks = *marks
				observation.usage = *sink
				observation.summary = w.Summary()
				return observation
			},
		},
		{
			name: "ResponsesToOpenAIChatWriter",
			fixtures: []streamFixture{
				{
					name: "reasoning, text and function_call turn", body: responsesStreamFixture, streaming: true,
					mustContain:  []string{`"reasoning_content":"Checking the weather "`, `"id":"call_xyz"`, `"name":"get_weather"`, `{\"location\":\"NYC\"}`, `"finish_reason":"tool_calls"`, "data: [DONE]"},
					wantProgress: true,
				},
				{
					name: "truncated before response.completed", body: responsesStreamPrefix(), streaming: true,
					mustContain: []string{`"type":"api_error"`, "data: [DONE]"}, wantFinalizeErr: translate.ErrStreamIncomplete, wantProgress: true,
				},
				{
					name: "non-streaming client renders one body", body: responsesStreamFixture,
					mustContain: []string{`"content":"Let me check the weather."`, `"id":"call_xyz"`, `"finish_reason":"tool_calls"`},
				},
				{name: "large text and tool arguments", body: largeResponsesStream(), streaming: true, mustContain: []string{`"name":"write_file"`, "call_large", `"finish_reason":"tool_calls"`, "data: [DONE]"}, wantProgress: true},
			},
			run: func(t *testing.T, fixture streamFixture, chunks [][]byte) streamObservation {
				rec := httptest.NewRecorder()
				sink := &fakeUsageSink{}
				w := translate.NewResponsesToOpenAIChatWriter(rec, "gpt-5.5", sink)
				preludeOrCommit(t, w, fixture.streaming)
				marks := armProgress(t, w, fixture.streaming)
				observation := runFinalizing(w, rec, chunks)
				observation.progressMarks = *marks
				observation.usage = *sink
				observation.summary = w.Summary()
				return observation
			},
		},
		{
			name: "AnthropicRoutingFooterWriter",
			fixtures: []streamFixture{
				{name: "natural answer", body: anthropicAnswerStream("end_turn"), streaming: true, mustContain: []string{"Weave Router feedback", "event: message_stop"}},
				{name: "natural answer with CRLF framing", body: withCRLF(anthropicAnswerStream("end_turn")), streaming: true, mustContain: []string{"Weave Router feedback", "event: message_stop\r\n"}},
				{name: "tool_use turn keeps footer off", body: anthropicToolTurnStream(), streaming: true, mustContain: []string{`"id":"toolu_frag_01"`, "event: message_stop"}, mustNotContain: []string{"Weave Router feedback"}},
				{name: "large text turn", body: largeAnthropicStream(), streaming: true, mustContain: []string{"Weave Router feedback", "event: message_stop"}},
			},
			run: func(t *testing.T, fixture streamFixture, chunks [][]byte) streamObservation {
				rec := httptest.NewRecorder()
				w := translate.NewAnthropicRoutingFooterWriter(rec, testFooter)
				commitUpstreamResponse(w, fixture.streaming)
				return runPassthrough(w, rec, chunks)
			},
		},
		{
			name: "OpenAIRoutingFooterWriter",
			fixtures: []streamFixture{
				{name: "natural answer", body: openAIAnswerStream("stop"), streaming: true, mustContain: []string{"Weave Router feedback", "data: [DONE]"}},
				{name: "tool_calls turn keeps footer off", body: openAIChunkToolTurnFrames().join(), streaming: true, mustContain: []string{`"id":"call_frag_1"`, "data: [DONE]"}, mustNotContain: []string{"Weave Router feedback"}},
				{name: "large text turn", body: largeOpenAIChunkStream(), streaming: true, mustContain: []string{"Weave Router feedback", "data: [DONE]"}},
			},
			run: func(t *testing.T, fixture streamFixture, chunks [][]byte) streamObservation {
				rec := httptest.NewRecorder()
				w := translate.NewOpenAIRoutingFooterWriter(rec, testFooter)
				commitUpstreamResponse(w, fixture.streaming)
				return runPassthrough(w, rec, chunks)
			},
		},
		{
			name: "GeminiRoutingFooterWriter",
			fixtures: []streamFixture{
				{name: "natural answer", body: geminiAnswerStream(), streaming: true, mustContain: []string{"Weave Router feedback", `"finishReason":"STOP"`}},
				{name: "functionCall turn keeps footer off", body: geminiToolTurnFrames().join(), streaming: true, mustContain: []string{`"name":"get_weather"`}, mustNotContain: []string{"Weave Router feedback"}},
				{name: "large text turn", body: largeGeminiStream(), streaming: true, mustContain: []string{"Weave Router feedback", `"finishReason":"STOP"`}},
			},
			run: func(t *testing.T, fixture streamFixture, chunks [][]byte) streamObservation {
				rec := httptest.NewRecorder()
				w := translate.NewGeminiRoutingFooterWriter(rec, testFooter)
				commitUpstreamResponse(w, fixture.streaming)
				return runPassthrough(w, rec, chunks)
			},
		},
		{
			name: "AnthropicRoutingMarkerWriter",
			fixtures: []streamFixture{
				{
					name: "text and tool_use turn", body: anthropicToolTurnStream(), streaming: true,
					mustContain: []string{"Weave Router", `"id":"toolu_frag_01"`, `"content_block_start","index":2`, `"input_tokens":12`, "event: message_stop"}, wantProgress: true,
				},
				{name: "large text turn", body: largeAnthropicStream(), streaming: true, mustContain: []string{"Weave Router", "event: message_stop"}, wantProgress: true},
			},
			run: func(t *testing.T, fixture streamFixture, chunks [][]byte) streamObservation {
				rec := httptest.NewRecorder()
				w := translate.NewAnthropicRoutingMarkerWriter(rec, "claude-opus-4", testMarker)
				commitUpstreamResponse(w, fixture.streaming)
				marks := armProgress(t, w, fixture.streaming)
				observation := runPassthrough(w, rec, chunks)
				observation.progressMarks = *marks
				return observation
			},
		},
		{
			name: "OpenAIRoutingMarkerWriter",
			fixtures: []streamFixture{
				{name: "natural answer", body: openAIAnswerStream("stop"), streaming: true, mustContain: []string{"Weave Router", `"finish_reason":"stop"`, "data: [DONE]"}, wantProgress: true},
				{name: "tool_calls turn", body: openAIChunkToolTurnFrames().join(), streaming: true, mustContain: []string{"Weave Router", `"id":"call_frag_1"`, "data: [DONE]"}, wantProgress: true},
				{name: "large text turn", body: largeOpenAIChunkStream(), streaming: true, mustContain: []string{"Weave Router", "data: [DONE]"}, wantProgress: true},
			},
			run: func(t *testing.T, fixture streamFixture, chunks [][]byte) streamObservation {
				rec := httptest.NewRecorder()
				w := translate.NewOpenAIRoutingMarkerWriter(rec, "gpt-x", testMarker)
				commitUpstreamResponse(w, fixture.streaming)
				marks := armProgress(t, w, fixture.streaming)
				observation := runPassthrough(w, rec, chunks)
				observation.progressMarks = *marks
				return observation
			},
		},
	}
}

// --- fragmentations ---

// fragmentation is one way of cutting a body into writes; runs holds every
// chunk sequence to try (the two-way split enumerates one per cut).
type fragmentation struct {
	name string
	runs [][][]byte
}

func chunkEvery(body []byte, size int) [][]byte {
	var chunks [][]byte
	for start := 0; start < len(body); start += size {
		chunks = append(chunks, body[start:min(start+size, len(body))])
	}
	return chunks
}

// fragmentations lists the write patterns to try for body. A non-streaming
// body is only buffered, so two chunkings prove that; a streaming body under
// everySplitMaxBytes gets every two-way split, longer ones the provider-sized
// chunkings.
func fragmentations(body []byte, streaming bool) []fragmentation {
	frags := []fragmentation{
		{name: "one byte per write", runs: [][][]byte{chunkEvery(body, 1)}},
		{name: "61-byte writes", runs: [][][]byte{chunkEvery(body, 61)}},
	}
	if !streaming {
		return frags
	}
	frags = append(frags,
		fragmentation{name: "7-byte writes", runs: [][][]byte{chunkEvery(body, 7)}},
		fragmentation{name: "13-byte writes", runs: [][][]byte{chunkEvery(body, 13)}},
	)
	if len(body) <= everySplitMaxBytes {
		runs := make([][][]byte, 0, len(body))
		for cut := 1; cut < len(body); cut++ {
			runs = append(runs, [][]byte{body[:cut], body[cut:]})
		}
		return append(frags, fragmentation{name: "every two-way split", runs: runs})
	}
	return append(frags,
		fragmentation{name: "4 KiB writes", runs: [][][]byte{chunkEvery(body, 4096)}},
		fragmentation{name: "4093-byte writes", runs: [][][]byte{chunkEvery(body, 4093)}},
	)
}

func describeChunks(chunks [][]byte) string {
	if len(chunks) == 2 {
		return fmt.Sprintf("split after %d bytes", len(chunks[0]))
	}
	return fmt.Sprintf("%d writes of up to %d bytes", len(chunks), len(chunks[0]))
}

// --- projection of per-instance generated values ---

var (
	generatedChatCmplID = regexp.MustCompile(`^chatcmpl-[0-9a-f]{16}$`)
	generatedMessageID  = regexp.MustCompile(`^msg_(?:translated_|responses_)?[0-9a-f]{16}$`)
	// toolUseIDNonce is the per-response suffix AnthropicSSETranslator adds to
	// every tool_use id, optionally followed by an embedded thought signature.
	toolUseIDNonce = regexp.MustCompile(`_[0-9a-f]{12}(__thought__.*)?$`)
	// generatedCallID is the random id the Gemini translator mints for a
	// functionCall, which may carry an embedded thought signature after it.
	generatedCallID = regexp.MustCompile(`^call_[0-9a-f]{8}`)
)

// projectGeneratedValues replaces only the values a writer generates per
// instance — random chat/message ids, wall-clock created stamps, the
// per-response tool_use id nonce, and minted tool-call ids — so two runs of
// the same input compare byte for byte on everything else. Sequence numbers,
// indexes, signatures, tool arguments, and ordering are never touched.
func projectGeneratedValues(t *testing.T, body string) string {
	t.Helper()
	if gjson.Valid(body) {
		return projectGeneratedJSON(t, body)
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || !gjson.Valid(data) {
			continue
		}
		lines[i] = "data: " + projectGeneratedJSON(t, data)
	}
	return strings.Join(lines, "\n")
}

func projectGeneratedJSON(t *testing.T, doc string) string {
	t.Helper()
	if id := gjson.Get(doc, "id"); id.Type == gjson.String {
		doc = replaceIfProjected(t, doc, "id", id.Str, placeholderForGeneratedID(id.Str))
	}
	if id := gjson.Get(doc, "message.id"); id.Type == gjson.String {
		doc = replaceIfProjected(t, doc, "message.id", id.Str, placeholderForGeneratedID(id.Str))
	}
	if gjson.Get(doc, "created").Type == gjson.Number {
		updated, err := sjson.Set(doc, "created", 0)
		require.NoError(t, err)
		doc = updated
	}
	if block := gjson.Get(doc, "content_block"); block.Get("type").Str == "tool_use" {
		id := block.Get("id").Str
		doc = replaceIfProjected(t, doc, "content_block.id", id, toolUseIDNonce.ReplaceAllString(id, "_NONCE$1"))
	}
	for i, call := range gjson.Get(doc, "choices.0.delta.tool_calls").Array() {
		if id := call.Get("id"); id.Type == gjson.String {
			doc = replaceIfProjected(t, doc, "choices.0.delta.tool_calls."+strconv.Itoa(i)+".id", id.Str, generatedCallID.ReplaceAllString(id.Str, "call_GENERATED"))
		}
	}
	return doc
}

func placeholderForGeneratedID(id string) string {
	switch {
	case generatedChatCmplID.MatchString(id):
		return "chatcmpl-GENERATED"
	case generatedMessageID.MatchString(id):
		return "msg_GENERATED"
	default:
		return id
	}
}

// replaceIfProjected rewrites path only when projection changed the value, so
// a document with nothing generated in it keeps its exact bytes.
func replaceIfProjected(t *testing.T, doc, path, current, projected string) string {
	t.Helper()
	if projected == current {
		return doc
	}
	updated, err := sjson.Set(doc, path, projected)
	require.NoError(t, err)
	return updated
}

// --- tests ---

func TestStreamFragmentation_WriteBoundariesDoNotChangeObservations(t *testing.T) {
	for _, family := range streamFamilies() {
		for _, fixture := range family.fixtures {
			t.Run(family.name+"/"+fixture.name, func(t *testing.T) {
				body := []byte(fixture.body)
				reference := family.run(t, fixture, [][]byte{body})
				fixture.checkReference(t, reference)
				want := reference.comparable(t)

				for _, frag := range fragmentations(body, fixture.streaming) {
					t.Run(frag.name, func(t *testing.T) {
						for _, chunks := range frag.runs {
							got := family.run(t, fixture, chunks).comparable(t)
							require.Equal(t, want, got, describeChunks(chunks))
						}
					})
				}
			})
		}
	}
}

// TestStreamFragmentation_ProjectionOnlyTouchesGeneratedValues guards the
// comparison itself: upstream-supplied ids, sequence fields, signatures, and
// arguments must survive projection unchanged, and only generated shapes are
// replaced.
func TestStreamFragmentation_ProjectionOnlyTouchesGeneratedValues(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "upstream ids and arguments are kept",
			in:   `data: {"id":"chatcmpl-frag","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_xyz__thought__U0lH","function":{"arguments":"{\"a\":1}"}}]}}]}`,
			want: `data: {"id":"chatcmpl-frag","created":0,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_xyz__thought__U0lH","function":{"arguments":"{\"a\":1}"}}]}}]}`,
		},
		{
			name: "generated chat id and minted call id are replaced, signature kept",
			in:   `data: {"id":"chatcmpl-0123456789abcdef","created":1700000000,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_0badf00d__thought__U0lH"}]}}]}`,
			want: `data: {"id":"chatcmpl-GENERATED","created":0,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_GENERATED__thought__U0lH"}]}}]}`,
		},
		{
			name: "generated message id is replaced, upstream one kept",
			in:   "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_translated_0123456789abcdef\"}}\n\nevent: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_frag_1\"}}\n\n",
			want: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_GENERATED\"}}\n\nevent: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_frag_1\"}}\n\n",
		},
		{
			name: "tool_use nonce is replaced, index and signature kept",
			in:   `data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_frag_1_0123456789ab__thought__U0lH","name":"f"}}`,
			want: `data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_frag_1_NONCE__thought__U0lH","name":"f"}}`,
		},
		{
			name: "non-JSON data and comments pass through",
			in:   ": keepalive\n\ndata: [DONE]\n\n",
			want: ": keepalive\n\ndata: [DONE]\n\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, projectGeneratedValues(t, tc.in))
		})
	}
}

// TestSSETranslator_ErroringEventIsConsumedBeforeTheErrorReturns pins the
// consume-even-on-error ordering: the frame that failed is dropped, so a
// later write continues with the frames buffered behind it instead of
// failing on the same frame again.
func TestSSETranslator_ErroringEventIsConsumedBeforeTheErrorReturns(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewSSETranslator(rec, "claude-opus-4", nil)
	commitUpstreamResponse(w, true)

	outOfOrderDelta := buildAnthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"early"}}`)
	frames := anthropicToolTurnFrames()

	_, err := w.Write([]byte(outOfOrderDelta + frames[0]))
	require.ErrorIs(t, err, translate.ErrStreamOrder)
	assert.Empty(t, rec.Body.String(), "a delta before message_start must produce no output")

	_, err = w.Write(nil)
	require.NoError(t, err, "the failed frame was consumed, so the buffered message_start must now translate")
	assert.Contains(t, rec.Body.String(), `"role":"assistant"`)
}

var errInnerWrite = errors.New("inner write failed")

// failingInner fails a fixed number of writes, then forwards to the recorder.
type failingInner struct {
	*httptest.ResponseRecorder
	failuresLeft int
}

func (w *failingInner) Write(p []byte) (int, error) {
	if w.failuresLeft > 0 {
		w.failuresLeft--
		return 0, errInnerWrite
	}
	return w.ResponseRecorder.Write(p)
}

// TestAnthropicRoutingMarkerWriter_WriteErrorBeforeConsumeRepresentsTheEvent
// covers an owner that returns a write error before consuming the framed
// event: the next Write must present that same event again, exactly once.
func TestAnthropicRoutingMarkerWriter_WriteErrorBeforeConsumeRepresentsTheEvent(t *testing.T) {
	inner := &failingInner{ResponseRecorder: httptest.NewRecorder()}
	w := translate.NewAnthropicRoutingMarkerWriter(inner, "claude-opus-4", testMarker)
	commitUpstreamResponse(w, true)

	_, err := w.Write([]byte(anthropicToolTurnFrames()[0]))
	require.NoError(t, err)
	preludeEvents := len(splitSSEEvents(inner.Body.String()))
	require.Equal(t, 4, preludeEvents, "the marker prelude is message_start plus a three-event text block")

	inner.failuresLeft = 1
	_, err = w.Write([]byte(buildAnthropicSSE("ping", `{"type":"ping"}`)))
	require.ErrorIs(t, err, errInnerWrite)
	require.Len(t, splitSSEEvents(inner.Body.String()), preludeEvents, "the failed ping must not reach the client")

	_, err = w.Write(nil)
	require.NoError(t, err)
	events := splitSSEEvents(inner.Body.String())
	require.Len(t, events, preludeEvents+1, "the unconsumed ping must be emitted exactly once on the next write")
	assert.Contains(t, events[len(events)-1], "event: ping")
}
