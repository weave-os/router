package translate_test

import (
	"testing"

	"workweave/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A native /v1/responses turn is forwarded verbatim, so the upstream's terminal
// event is the only statement of how it ended. These pin the mapping the proxy
// records for those turns.
func TestResponsesTerminalReason(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    string
		wantOK  bool
	}{
		{
			name:    "tool call turn",
			payload: `{"type":"response.completed","sequence_number":9,"response":{"id":"resp_1","status":"completed","model":"gpt-5.6-sol","output":[{"id":"rs_1","type":"reasoning","summary":[]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"ls\"}","status":"completed"}],"usage":{"input_tokens":120,"output_tokens":34}}}`,
			want:    "tool_calls",
			wantOK:  true,
		},
		{
			name:    "completed answer",
			payload: `{"type":"response.completed","sequence_number":7,"response":{"id":"resp_1","status":"completed","model":"gpt-5.6-sol","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":120,"output_tokens":8}}}`,
			want:    "stop",
			wantOK:  true,
		},
		{
			name:    "output cap reached",
			payload: `{"type":"response.incomplete","sequence_number":5,"response":{"id":"resp_1","status":"incomplete","model":"gpt-5.6-sol","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}]}}`,
			want:    "length",
			wantOK:  true,
		},
		{
			name:    "failed terminal reports no reason",
			payload: `{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"boom"}}}`,
			wantOK:  false,
		},
		{
			name:    "incomplete for a non-cap reason reports no reason",
			payload: `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`,
			wantOK:  false,
		},
		{
			name:    "mid-stream event is not terminal",
			payload: `{"type":"response.output_text.delta","output_index":0,"delta":"partial"}`,
			wantOK:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := translate.ResponsesTerminalReason([]byte(tc.payload))
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A native non-streaming body is the response object itself: no SSE framing,
// no envelope "type", no nested "response". The observer hands it here whole.
func TestResponsesTerminalReason_NonStreamingBody(t *testing.T) {
	body := []byte(`{"id":"resp_1","object":"response","status":"completed",` +
		`"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],` +
		`"usage":{"input_tokens":10,"output_tokens":3}}`)

	reason, ok := translate.ResponsesTerminalReason(body)
	require.True(t, ok, "a non-streaming native body states the turn's outcome")
	assert.Equal(t, "stop", reason)
}

// Codex native turns emit custom_tool_call for shell-style tools; those end the
// turn in a tool call exactly as function_call does.
func TestResponsesTerminalReason_CustomToolCallIsToolCalls(t *testing.T) {
	payload := []byte(`{"type":"response.completed","response":{"id":"r","status":"completed",` +
		`"output":[{"type":"custom_tool_call","call_id":"c1","name":"shell","input":"ls -la"}],` +
		`"usage":{"input_tokens":10,"output_tokens":5}}}`)

	reason, ok := translate.ResponsesTerminalReason(payload)
	require.True(t, ok)
	assert.Equal(t, "tool_calls", reason)
}

func TestResponsesTerminalReason_NonStreamingCustomToolCall(t *testing.T) {
	body := []byte(`{"id":"resp_1","object":"response","status":"completed",` +
		`"output":[{"type":"custom_tool_call","call_id":"c1","name":"shell","input":"ls"}]}`)

	reason, ok := translate.ResponsesTerminalReason(body)
	require.True(t, ok)
	assert.Equal(t, "tool_calls", reason)
}

// A non-terminal streaming frame must stay unrecorded even though the
// non-streaming path now accepts envelope-less payloads.
func TestResponsesTerminalReason_NonStreamingRejectsNonTerminal(t *testing.T) {
	_, ok := translate.ResponsesTerminalReason([]byte(`{"type":"response.output_text.delta","delta":"hi"}`))
	assert.False(t, ok, "a delta frame states no outcome")

	_, ok = translate.ResponsesTerminalReason([]byte(`{"id":"resp_1","object":"response","status":"in_progress"}`))
	assert.False(t, ok, "an in-progress body states no outcome")
}
