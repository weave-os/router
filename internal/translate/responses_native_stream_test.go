package translate_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type nativeTerminalStatus string

const (
	nativeCompletedStatus  nativeTerminalStatus = "completed"
	nativeIncompleteStatus nativeTerminalStatus = "incomplete"
)

func nativeStreamFrame(payload string) string {
	return "event: " + gjson.Get(payload, "type").Str + "\ndata: " + payload + "\n\n"
}

func newNativeStreamWriter(display bool) (*translate.ResponsesWriter, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesWriter(rec, "gpt-5.6-luna")
	if display {
		w.SetPassthroughBadge()
	} else {
		w.SetPassthrough()
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	return w, rec
}

func TestResponsesWriter_NativeSparseTerminalAfterUsableOutput(t *testing.T) {
	for _, output := range []struct {
		name    string
		payload string
	}{
		{"text delta", `{"type":"response.output_text.delta","delta":"answer"}`},
		{"text snapshot", `{"type":"response.output_text.done","text":"answer"}`},
		{"content part", `{"type":"response.content_part.done","part":{"type":"output_text","text":"answer"}}`},
		{"message item", `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"answer"}]}}`},
		{"function call", `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":""}}`},
		{"custom tool", `{"type":"response.output_item.added","item":{"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"shell","input":""}}`},
		{"completed custom tool", `{"type":"response.output_item.done","item":{"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"shell","input":"pwd"}}`},
		{"computer call", `{"type":"response.output_item.done","item":{"type":"computer_call","call_id":"call_1","action":{"type":"click"}}}`},
		{"refusal delta", `{"type":"response.refusal.delta","delta":"Cannot do that"}`},
		{"refusal part", `{"type":"response.content_part.done","part":{"type":"refusal","refusal":"Cannot do that"}}`},
		{"reasoning summary", `{"type":"response.reasoning_summary_text.delta","delta":"Checking the result"}`},
	} {
		for _, display := range []bool{false, true} {
			for _, status := range []nativeTerminalStatus{nativeCompletedStatus, nativeIncompleteStatus} {
				for _, delimited := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/display=%t/%s/delimited=%t", output.name, display, status, delimited), func(t *testing.T) {
						w, rec := newNativeStreamWriter(display)
						if display {
							w.SetBadgeText(passthroughTestMarker)
							w.SetFooterText("feedback")
							// Covers content seen before footer snapshots are released.
						}
						native := nativeStreamFrame(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_native","status":"in_progress","output":[]}}`) +
							nativeStreamFrame(output.payload) +
							nativeStreamFrame(fmt.Sprintf(`{"type":"response.%s","response":{"id":"resp_native","status":"%s","output":[]}}`, status, status))
						if !delimited {
							native = strings.TrimSuffix(native, "\n\n")
						}
						for start := 0; start < len(native); start += 17 {
							_, err := w.Write([]byte(native[start:min(start+17, len(native))]))
							require.NoError(t, err)
						}
						require.NoError(t, w.Finalize())
						beforeError := rec.Body.String()
						require.NoError(t, w.FinalizeError(errors.New("late upstream error")))
						assert.Equal(t, beforeError, rec.Body.String(), "never append a second terminal")
						events := parseSSEEvents(t, rec.Body.Bytes())
						terminal := events[len(events)-1]
						assert.Equal(t, "response."+string(status), terminal["type"])
						assert.Equal(t, "resp_native", terminal["response"].(map[string]any)["id"])
						if !display {
							assert.Equal(t, native, rec.Body.String())
						}
					})
				}
			}
		}
	}
}

func TestResponsesWriter_NativeEmptyOutputStillRejected(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.output_text.delta","delta":""}`,
		`{"type":"response.output_item.added","item":{"type":"message","content":[]}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","encrypted_content":"opaque","summary":[]}}`,
		`{"type":"response.output_item.added","item":{"type":"custom_tool_call","name":""}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{}"}`,
		`{"type":"response.custom_tool_call_input.delta","delta":"pwd"}`,
	} {
		for _, display := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/display=%t", payload, display), func(t *testing.T) {
				w, rec := newNativeStreamWriter(display)
				w.SetBadgeText(passthroughTestMarker)
				require.NoError(t, w.Prelude(true))
				_, err := w.Write([]byte(nativeStreamFrame(payload)))
				require.NoError(t, err)
				_, err = w.Write([]byte(nativeStreamFrame(`{"type":"response.completed","response":{"status":"completed","output":[]}}`)))
				require.ErrorIs(t, err, providers.ErrUpstreamEmptyCompletion)
				require.ErrorIs(t, w.Finalize(), providers.ErrUpstreamEmptyCompletion)
				require.NoError(t, w.FinalizeError(err))
				events := parseSSEEvents(t, rec.Body.Bytes())
				assert.Equal(t, "response.failed", events[len(events)-1]["type"])
				assert.NotContains(t, rec.Body.String(), `"type":"response.completed"`)
			})
		}
	}
}

func TestResponsesWriter_NativeFailurePreservesDeliveredIdentityAndSequence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		display bool
		badge   bool
		prelude bool
	}{
		{name: "native"},
		{name: "display without badge", display: true},
		{name: "display with inserted badge", display: true, badge: true},
		{name: "display with prelude", display: true, badge: true, prelude: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, rec := newNativeStreamWriter(tc.display)
			if tc.badge {
				w.SetBadgeText(passthroughTestMarker)
			}
			if tc.prelude {
				require.NoError(t, w.Prelude(true))
			}
			_, err := w.Write([]byte(nativeStreamFrame(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_native","status":"in_progress","output":[]}}`) +
				nativeStreamFrame(`{"type":"response.output_item.added","sequence_number":1,"item":{"type":"custom_tool_call","name":"shell","id":"ctc_1","call_id":"call_1"}}`)))
			require.NoError(t, err)
			before := parseSSEEvents(t, rec.Body.Bytes())
			require.NoError(t, w.FinalizeError(errors.New("upstream disconnected")))
			require.NoError(t, w.FinalizeError(errors.New("repeated cleanup")))
			events := parseSSEEvents(t, rec.Body.Bytes())
			require.Len(t, events, len(before)+1)
			failed := events[len(events)-1]
			assert.Equal(t, "response.failed", failed["type"])
			assert.Equal(t, before[len(before)-1]["sequence_number"].(float64)+1, failed["sequence_number"])
			assert.Equal(t, before[0]["response"].(map[string]any)["id"], failed["response"].(map[string]any)["id"])
		})
	}
}

func TestResponsesWriter_NativeUpstreamFailureIsNotDuplicated(t *testing.T) {
	for _, display := range []bool{false, true} {
		w, rec := newNativeStreamWriter(display)
		failed := nativeStreamFrame(`{"type":"response.failed","sequence_number":2,"response":{"id":"resp_native","status":"failed","output":[],"error":{"message":"unavailable"}}}`)
		_, err := w.Write([]byte(failed))
		require.NoError(t, err)
		require.NoError(t, w.FinalizeError(errors.New("upstream failed")))
		assert.Equal(t, failed, rec.Body.String())
	}
}

func TestResponsesWriter_NativeAttemptResetDiscardsOutputAndTerminalState(t *testing.T) {
	for _, display := range []bool{false, true} {
		w, rec := newNativeStreamWriter(display)
		_, err := w.Write([]byte(nativeStreamFrame(`{"type":"response.output_text.delta","delta":"prior attempt"}`) +
			nativeStreamFrame(`{"type":"response.failed","sequence_number":50,"response":{"id":"resp_old","status":"failed","output":[]}}`)))
		require.NoError(t, err)
		w.ResetAttempt()
		rec.Body.Reset()
		require.NoError(t, w.FinalizeError(errors.New("nothing streamed in new attempt")))
		assert.Empty(t, rec.Body.String())
		_, err = w.Write([]byte(nativeStreamFrame(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_new","status":"in_progress","output":[]}}`) +
			nativeStreamFrame(`{"type":"response.completed","sequence_number":1,"response":{"id":"resp_new","status":"completed","output":[]}}`)))
		require.ErrorIs(t, err, providers.ErrUpstreamEmptyCompletion)
		require.NoError(t, w.FinalizeError(err))
		events := parseSSEEvents(t, rec.Body.Bytes())
		require.Len(t, events, 2)
		assert.Equal(t, "response.failed", events[1]["type"])
		assert.EqualValues(t, 1, events[1]["sequence_number"])
		assert.Equal(t, "resp_new", events[1]["response"].(map[string]any)["id"])
	}
}

func TestResponsesWriter_NativeAttemptResetPreservesSyntheticPrelude(t *testing.T) {
	w, rec := newNativeStreamWriter(true)
	w.SetBadgeText(passthroughTestMarker)
	require.NoError(t, w.Prelude(true))
	w.ResetAttempt()
	_, err := w.Write([]byte(nativeStreamFrame(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_upstream","status":"in_progress","output":[]}}`)))
	require.NoError(t, err)
	require.NoError(t, w.FinalizeError(errors.New("empty attempt failed")))
	events := parseSSEEvents(t, rec.Body.Bytes())
	require.Len(t, events, 8)
	assert.Equal(t, "response.failed", events[7]["type"])
	assert.EqualValues(t, 7, events[7]["sequence_number"])
	assert.Equal(t, events[0]["response"].(map[string]any)["id"], events[7]["response"].(map[string]any)["id"])
}
