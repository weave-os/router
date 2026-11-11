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
	_ = driveSSE(t, w, events...)
	got := w.Summary()
	assert.Empty(t, got.UpstreamFinishReason)
	assert.Empty(t, got.StopReason, "a cut stream must stay unknown, not report a measured stop")
}
