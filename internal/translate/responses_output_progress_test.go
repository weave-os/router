package translate_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Output and reasoning are separate signals: only output contributes to
// first-output latency and throughput, while both can reset the stall clock.

// SSE events lifted from responsesStreamFixture, one per const so a test can
// feed them individually and assert the mark fires (or not) per event.
const (
	evReasoningItemAdded = `event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","encrypted_content":"enc","summary":[],"status":"in_progress"}}

`
	evReasoningDelta = `event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"thinking"}

`
	evReasoningItemDone = `event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","encrypted_content":"enc","summary":[{"type":"summary_text","text":"thinking"}],"status":"completed"}}

`
	evMessageItemAdded = `event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}

`
	evTextDelta = `event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"hi"}

`
	evToolArgsDelta = `event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"{\"q\":1}"}

`
	evCompleted = `event: response.completed
data: {"type":"response.completed","response":{"id":"resp_abc","status":"completed","model":"gpt-5.5","incomplete_details":null,"output":[],"usage":{"input_tokens":1,"output_tokens":1}}}

`
)

func newStreamingWriter(t *testing.T) (*translate.ResponsesToAnthropicWriter, *int) {
	t.Helper()
	w := translate.NewResponsesToAnthropicWriter(httptest.NewRecorder(), "gpt-5.5", nil)
	require.NoError(t, w.Prelude(true))
	count := 0
	require.True(t, w.ArmOutputProgress(func() { count++ }),
		"ArmOutputProgress must report armed for a streaming client")
	return w, &count
}

type responsesProgressWriter interface {
	http.ResponseWriter
	Prelude(bool) error
	ArmOutputProgress(func()) bool
	ArmReasoningProgress(func()) bool
}

func TestResponsesReasoningProgress_Classification(t *testing.T) {
	constructors := map[string]func() responsesProgressWriter{
		"anthropic": func() responsesProgressWriter {
			return translate.NewResponsesToAnthropicWriter(httptest.NewRecorder(), "gpt-5.5", nil)
		},
		"chat": func() responsesProgressWriter {
			return translate.NewResponsesToOpenAIChatWriter(httptest.NewRecorder(), "gpt-5.5", nil)
		},
	}
	cases := []struct {
		name              string
		frame             string
		reasoning, output int
	}{
		{"summary delta", evReasoningDelta, 1, 0},
		{"reasoning text delta", `data: {"type":"response.reasoning_text.delta","output_index":0,"delta":"working"}`, 1, 0},
		{"completed summary", `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","summary":[{"text":"complete"}]}}`, 1, 0},
		{"completed opaque payload", `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"opaque","summary":[]}}`, 1, 0},
		{"summary and opaque count once", evReasoningItemDone, 1, 0},
		{"item opening is not progress", evReasoningItemAdded, 0, 0},
		{"empty delta", `data: {"type":"response.reasoning_summary_text.delta","delta":""}`, 0, 0},
		{"null delta", `data: {"type":"response.reasoning_text.delta","delta":null}`, 0, 0},
		{"non-string delta", `data: {"type":"response.reasoning_text.delta","delta":42}`, 0, 0},
		{"missing delta", `data: {"type":"response.reasoning_text.delta"}`, 0, 0},
		{"empty completion", `data: {"type":"response.output_item.done","item":{"type":"reasoning","summary":[]}}`, 0, 0},
		{"empty summary text", `data: {"type":"response.output_item.done","item":{"type":"reasoning","summary":[{"text":""}]}}`, 0, 0},
		{"missing opaque id", `data: {"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"opaque"}}`, 0, 0},
		{"empty opaque content", `data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","encrypted_content":""}}`, 0, 0},
		{"non-string opaque content", `data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","encrypted_content":42}}`, 0, 0},
		{"status", `data: {"type":"response.in_progress"}`, 0, 0},
		{"unknown", `data: {"type":"response.unknown","delta":"hi"}`, 0, 0},
		{"ping", ": ping", 0, 0},
		{"output keeps original signal", evTextDelta, 0, 1},
	}
	for name, newWriter := range constructors {
		t.Run(name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					w := newWriter()
					require.NoError(t, w.Prelude(true))
					reasoning, output := 0, 0
					require.True(t, w.ArmReasoningProgress(func() { reasoning++ }))
					require.True(t, w.ArmOutputProgress(func() { output++ }))
					frame := tc.frame + "\n\n"
					// A partial JSON frame must not register before it is complete.
					_, err := w.Write([]byte(frame[:len(frame)/2]))
					require.NoError(t, err)
					assert.Zero(t, reasoning)
					assert.Zero(t, output)
					_, err = w.Write([]byte(frame[len(frame)/2:]))
					require.NoError(t, err)
					assert.Equal(t, tc.reasoning, reasoning)
					assert.Equal(t, tc.output, output)
				})
			}
			t.Run("buffered clients decline both callbacks", func(t *testing.T) {
				w := newWriter()
				require.NoError(t, w.Prelude(false))
				assert.False(t, w.ArmOutputProgress(func() { t.Error("buffered output callback fired") }))
				assert.False(t, w.ArmReasoningProgress(func() { t.Error("buffered reasoning callback fired") }))
				_, err := w.Write([]byte(evReasoningDelta + evReasoningItemDone + evTextDelta))
				require.NoError(t, err)
			})
		})
	}
}

func TestResponsesOutputProgress_OutputEventsCount(t *testing.T) {
	tests := []struct {
		name  string
		event string
	}{
		{"message item added", evMessageItemAdded},
		{"text delta", evTextDelta},
		{"tool args delta", evToolArgsDelta},
		{"terminal completed", evCompleted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, count := newStreamingWriter(t)
			_, err := w.Write([]byte(tc.event))
			require.NoError(t, err)
			assert.Positive(t, *count, "%s must register as output progress", tc.name)
		})
	}
}

func TestResponsesOutputProgress_NotArmedWhenNotStreaming(t *testing.T) {
	// The buffered (non-streaming) path parses events only at Finalize, so it has
	// nothing to mark mid-stream; arming there would guarantee a false trip.
	w := translate.NewResponsesToAnthropicWriter(httptest.NewRecorder(), "gpt-5.5", nil)
	require.NoError(t, w.Prelude(false))
	assert.False(t, w.ArmOutputProgress(func() {}),
		"ArmOutputProgress must report not-armed for a non-streaming client")
}
