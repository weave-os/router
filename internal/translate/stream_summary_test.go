package translate_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

func TestSSETranslator_SummaryReportsTerminalSignals(t *testing.T) {
	cases := []struct {
		name       string
		stopReason string
		toolUse    bool
		wantFinish string
		wantTools  int
	}{
		{"tool turn", "tool_use", true, "tool_calls", 1},
		{"text turn", "end_turn", false, "stop", 0},
	}
	for _, tc := range cases {
		t.Run("streaming/"+tc.name, func(t *testing.T) {
			w := translate.NewSSETranslator(httptest.NewRecorder(), outputLimitAnthropicModel, &fakeUsageSink{})
			require.NoError(t, driveSSE(t, w, anthropicMessagesStream(tc.stopReason, 42, tc.toolUse)...))
			got := w.Summary()
			assert.Equal(t, tc.stopReason, got.UpstreamFinishReason)
			assert.Equal(t, tc.wantFinish, got.StopReason)
			assert.Equal(t, tc.wantTools, got.ToolUseBlocks)
			assert.Equal(t, 42, got.OutputTokens)
		})
		t.Run("buffered/"+tc.name, func(t *testing.T) {
			w := translate.NewSSETranslator(httptest.NewRecorder(), outputLimitAnthropicModel, &fakeUsageSink{})
			require.NoError(t, driveJSON(t, w, anthropicMessagesBody(tc.stopReason, 42, tc.toolUse)))
			got := w.Summary()
			assert.Equal(t, tc.stopReason, got.UpstreamFinishReason)
			assert.Equal(t, tc.wantFinish, got.StopReason)
			assert.Equal(t, tc.wantTools, got.ToolUseBlocks)
		})
	}
}

func TestSSETranslator_SummaryUnknownWithoutTerminal(t *testing.T) {
	var events []string
	for _, e := range anthropicMessagesStream("tool_use", 42, true) {
		if !strings.Contains(e, "message_delta") && !strings.Contains(e, "message_stop") {
			events = append(events, e)
		}
	}
	w := translate.NewSSETranslator(httptest.NewRecorder(), outputLimitAnthropicModel, &fakeUsageSink{})
	require.ErrorIs(t, driveSSE(t, w, events...), translate.ErrStreamIncomplete)
	got := w.Summary()
	assert.Empty(t, got.UpstreamFinishReason)
	assert.Empty(t, got.StopReason, "a cut stream must stay unknown, not report a measured stop")
}

func TestSSETranslator_SummaryUnknownWithoutMessageStop(t *testing.T) {
	events := anthropicMessagesStream("tool_use", 42, true)
	events = events[:len(events)-1]
	w := translate.NewSSETranslator(httptest.NewRecorder(), outputLimitAnthropicModel, &fakeUsageSink{})
	require.ErrorIs(t, driveSSE(t, w, events...), translate.ErrStreamIncomplete)
	got := w.Summary()
	assert.Empty(t, got.UpstreamFinishReason, "message_delta alone does not show the upstream finished the stream")
	assert.Empty(t, got.StopReason)
}

func TestSSETranslator_SummaryBufferedWithoutStopReason(t *testing.T) {
	body := strings.Replace(anthropicMessagesBody("end_turn", 42, false), `"stop_reason":"end_turn"`, `"stop_reason":null`, 1)
	w := translate.NewSSETranslator(httptest.NewRecorder(), outputLimitAnthropicModel, &fakeUsageSink{})
	require.NoError(t, driveJSON(t, w, body))
	got := w.Summary()
	assert.Empty(t, got.UpstreamFinishReason)
	assert.Empty(t, got.StopReason, "the client-facing default stop must not be recorded as an observed one")
}

func TestSSETranslator_SummaryCountsInvalidToolArgs(t *testing.T) {
	events := anthropicMessagesStream("tool_use", 42, true)
	for i, e := range events {
		events[i] = strings.Replace(e, `"partial_json":"{\"path\":\"a.go\"}"`, `"partial_json":"{\"path\":"`, 1)
	}
	w := translate.NewSSETranslator(httptest.NewRecorder(), outputLimitAnthropicModel, &fakeUsageSink{})
	require.NoError(t, driveSSE(t, w, events...))
	got := w.Summary()
	assert.Equal(t, 1, got.InvalidToolArgsBlocks)
	assert.Len(t, got.ToolCallIssues, 1)
}

func TestSSETranslator_SummaryBufferedCountsSuppressedToolCalls(t *testing.T) {
	body := strings.Replace(anthropicMessagesBody("tool_use", 42, true), `"name":"Edit"`, `"name":""`, 1)
	w := translate.NewSSETranslator(httptest.NewRecorder(), outputLimitAnthropicModel, &fakeUsageSink{})
	require.NoError(t, driveJSON(t, w, body))
	got := w.Summary()
	assert.Equal(t, 1, got.SuppressedToolCalls)
	assert.Equal(t, 0, got.ToolUseBlocks)
}
