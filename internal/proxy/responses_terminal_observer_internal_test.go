package proxy

import (
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A native /v1/responses turn that ends in one function call.
const responsesTerminalToolStream = "event: response.created\n" +
	`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}` + "\n\n" +
	"event: response.output_item.added\n" +
	`data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":""}}` + "\n\n" +
	"event: response.function_call_arguments.delta\n" +
	`data: {"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_1","delta":"{\"path\":\"a.go\"}"}` + "\n\n" +
	"event: response.output_item.done\n" +
	`data: {"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a.go\"}","status":"completed"}}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","sequence_number":4,"response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a.go\"}","status":"completed"}],"usage":{"input_tokens":10,"output_tokens":5}}}` + "\n\n"

// A text turn the upstream cut at its output-token cap.
const responsesTerminalCappedStream = "event: response.created\n" +
	`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_2","status":"in_progress"}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","sequence_number":1,"output_index":0,"delta":"partial answer"}` + "\n\n" +
	"event: response.incomplete\n" +
	`data: {"type":"response.incomplete","sequence_number":2,"response":{"id":"resp_2","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial answer"}]}],"usage":{"input_tokens":10,"output_tokens":64}}}` + "\n\n"

const responsesTerminalNonStreamingBody = `{"id":"resp_3","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`

var (
	responsesTerminalToolSignals   = translate.ResponsesTerminalSignals{FinishReason: "tool_calls", ToolCalls: 1}
	responsesTerminalCappedSignals = translate.ResponsesTerminalSignals{FinishReason: "length", OutputLimitReached: true}
	responsesTerminalStopSignals   = translate.ResponsesTerminalSignals{FinishReason: "stop"}
)

// responsesTerminalState is what the dispatch loop reads off the observer.
type responsesTerminalState struct {
	observed bool
	signals  translate.ResponsesTerminalSignals
}

func stateOf(o *responsesTerminalObserver) responsesTerminalState {
	return responsesTerminalState{observed: o.observed, signals: o.signals}
}

// responsesTerminalLargeStream pads the tool stream with a text delta larger
// than the adapters' 4 KiB read so the terminal frame arrives after many
// partial writes of one frame.
func responsesTerminalLargeStream() string {
	delta := "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","sequence_number":1,"output_index":0,"delta":"` + strings.Repeat("x", 64*1024) + `"}` + "\n\n"
	terminal := strings.Index(responsesTerminalToolStream, "event: response.output_item.added")
	return responsesTerminalToolStream[:terminal] + delta + responsesTerminalToolStream[terminal:]
}

func TestResponsesTerminalObserver_ReadsTerminalSignals(t *testing.T) {
	cases := []struct {
		name string
		body string
		want translate.ResponsesTerminalSignals
	}{
		{name: "tool call", body: responsesTerminalToolStream, want: responsesTerminalToolSignals},
		{name: "output cap", body: responsesTerminalCappedStream, want: responsesTerminalCappedSignals},
		{name: "crlf framing", body: strings.ReplaceAll(responsesTerminalToolStream, "\n", "\r\n"), want: responsesTerminalToolSignals},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			obs := newResponsesTerminalObserver(rec)
			writeChunks(t, obs, []string{tc.body})
			obs.Finalize()

			assert.Equal(t, responsesTerminalState{observed: true, signals: tc.want}, stateOf(obs))
			assert.Equal(t, tc.body, rec.Body.String(), "the observer must pass every byte through unchanged")
		})
	}
}

// The wire splits frames arbitrarily across writes. Whatever the split, the
// observer must report exactly what a single write of the same bytes reports,
// both after each write boundary and after Finalize.
func TestResponsesTerminalObserver_FragmentationMatchesSingleWrite(t *testing.T) {
	fixtures := []struct {
		name string
		body string
	}{
		{name: "lf tool stream", body: responsesTerminalToolStream},
		{name: "crlf tool stream", body: strings.ReplaceAll(responsesTerminalToolStream, "\n", "\r\n")},
		{name: "capped stream", body: responsesTerminalCappedStream},
		{name: "unterminated terminal", body: strings.TrimSuffix(responsesTerminalToolStream, "\n\n")},
		{name: "large frame ahead of the terminal", body: responsesTerminalLargeStream()},
	}
	prefixState := func(t *testing.T, prefix string) responsesTerminalState {
		t.Helper()
		obs := newResponsesTerminalObserver(httptest.NewRecorder())
		writeChunks(t, obs, []string{prefix})
		return stateOf(obs)
	}
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			oracle := newResponsesTerminalObserver(httptest.NewRecorder())
			writeChunks(t, oracle, []string{fx.body})
			oracle.Finalize()
			want := stateOf(oracle)
			require.True(t, want.observed, "fixture must reach a terminal so equivalence is not vacuous")

			for _, size := range chunkSizesFor(fx.body) {
				rec := httptest.NewRecorder()
				obs := newResponsesTerminalObserver(rec)
				writeChunks(t, obs, chunkEvery(fx.body, size))
				obs.Finalize()
				assert.Equal(t, want, stateOf(obs), "chunk size %d", size)
				assert.Equal(t, fx.body, rec.Body.String(), "chunk size %d", size)
			}

			if len(fx.body) > largeFixtureBytes {
				return
			}
			for i := 1; i < len(fx.body); i++ {
				rec := httptest.NewRecorder()
				obs := newResponsesTerminalObserver(rec)
				writeChunks(t, obs, []string{fx.body[:i]})
				require.Equal(t, prefixState(t, fx.body[:i]), stateOf(obs), "state after the first write, split at %d", i)
				writeChunks(t, obs, []string{fx.body[i:]})
				obs.Finalize()
				require.Equal(t, want, stateOf(obs), "split at %d", i)
				require.Equal(t, fx.body, rec.Body.String(), "split at %d", i)
			}
		})
	}
}

// A terminal frame the upstream never closed with a blank line is only
// readable once the call has returned, and a stream cut before the terminal
// states nothing at all.
func TestResponsesTerminalObserver_UnterminatedTerminalIsReadAtFinalize(t *testing.T) {
	unterminated := strings.TrimSuffix(responsesTerminalToolStream, "\n\n")
	obs := newResponsesTerminalObserver(httptest.NewRecorder())
	writeChunks(t, obs, chunkEvery(unterminated, 7))
	assert.False(t, obs.observed, "the terminal frame has no delimiter yet")

	obs.Finalize()
	assert.Equal(t, responsesTerminalState{observed: true, signals: responsesTerminalToolSignals}, stateOf(obs))

	cut := responsesTerminalToolStream[:strings.Index(responsesTerminalToolStream, "event: response.completed")]
	cutObs := newResponsesTerminalObserver(httptest.NewRecorder())
	writeChunks(t, cutObs, chunkEvery(cut, 13))
	cutObs.Finalize()
	assert.False(t, cutObs.observed)
}

// A non-streaming body completes no SSE frame however it is chunked; it is the
// bare response object and is classified at Finalize. Failed and in-progress
// bodies state no outcome.
func TestResponsesTerminalObserver_NonStreamingBodyIsReadAtFinalize(t *testing.T) {
	obs := newResponsesTerminalObserver(httptest.NewRecorder())
	writeChunks(t, obs, chunkEvery(responsesTerminalNonStreamingBody, 33))
	assert.False(t, obs.observed, "no frame completes before Finalize")
	obs.Finalize()
	assert.Equal(t, responsesTerminalState{observed: true, signals: responsesTerminalStopSignals}, stateOf(obs))

	for name, body := range map[string]string{
		"failed status":    `{"id":"resp_4","object":"response","status":"failed","error":{"code":"server_error","message":"boom"},"output":[]}`,
		"in-progress body": `{"id":"resp_5","object":"response","status":"in_progress","output":[]}`,
		"error envelope":   `{"error":{"type":"server_error","message":"upstream unavailable"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			obs := newResponsesTerminalObserver(httptest.NewRecorder())
			writeChunks(t, obs, chunkEvery(body, 7))
			obs.Finalize()
			assert.False(t, obs.observed)
		})
	}
}

// Finalize is a full stop: a second call with nothing buffered changes nothing,
// and frames written afterwards are framed from scratch rather than as a
// continuation of the tail Finalize already read.
func TestResponsesTerminalObserver_FinalizeIsIdempotent(t *testing.T) {
	obs := newResponsesTerminalObserver(httptest.NewRecorder())
	writeChunks(t, obs, []string{strings.TrimSuffix(responsesTerminalToolStream, "\n\n")})
	obs.Finalize()
	obs.Finalize()
	assert.Equal(t, responsesTerminalState{observed: true, signals: responsesTerminalToolSignals}, stateOf(obs))

	for _, frame := range strings.SplitAfter(responsesTerminalCappedStream, "\n\n") {
		writeChunks(t, obs, []string{frame})
	}
	assert.Equal(t, responsesTerminalState{observed: true, signals: responsesTerminalCappedSignals}, stateOf(obs),
		"a later terminal statement wins")
}

// The native passthrough runs no translator, so the observer is what keeps the
// adapters' output-progress watchdog reachable.
func TestResponsesTerminalObserver_ForwardsOutputProgressArming(t *testing.T) {
	armer := &armerRecorder{ResponseRecorder: httptest.NewRecorder()}
	assert.True(t, newResponsesTerminalObserver(armer).ArmOutputProgress(func() {}))
	assert.Equal(t, 1, armer.marks)

	assert.False(t, newResponsesTerminalObserver(httptest.NewRecorder()).ArmOutputProgress(func() {}),
		"a writer that cannot classify output frames must not claim to be armed")
}
