package translate_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

// Empty upstream turn (no content, no finish_reason): the shape that drew a
// nudge on every turn of a pinned session that never recovered.
var emptyUpstreamTurn = []string{
	`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n",
	"data: [DONE]\n\n",
}

func driveWithPriorNudges(t *testing.T, prior int) (string, translate.ResponseSummary) {
	t.Helper()
	rec := httptest.NewRecorder()
	tr := translate.NewAnthropicSSETranslator(rec, "inclusionai/ling-3.0-flash", nil).
		WithRequestHadTools(true).
		WithPriorConsecutiveNudges(prior)
	tr.Header().Set("Content-Type", "text/event-stream")
	tr.WriteHeader(http.StatusOK)
	for _, e := range emptyUpstreamTurn {
		_, err := tr.Write([]byte(e))
		require.NoError(t, err)
	}
	require.NoError(t, tr.Finalize())
	return rec.Body.String(), tr.Summary()
}

func TestTextOnlyTurnNudge_FiresBelowConsecutiveCap(t *testing.T) {
	body, summary := driveWithPriorNudges(t, 1)
	assert.True(t, summary.TextOnlyTurnNudged)
	assert.Contains(t, body, "toolu_router_nudge_")
	assert.Equal(t, "tool_use", summary.StopReason)
}

func TestTextOnlyTurnNudge_SuppressedAfterConsecutiveNudges(t *testing.T) {
	body, summary := driveWithPriorNudges(t, 2)
	assert.False(t, summary.TextOnlyTurnNudged, "a third consecutive nudge must not be synthesized")
	assert.NotContains(t, body, "toolu_router_nudge_")
	assert.Equal(t, 0, summary.ToolUseBlocks)
	assert.Equal(t, "end_turn", summary.StopReason, "the turn ends so the client returns control to the user")
}

func nudgeTurn(id string) []map[string]any {
	return []map[string]any{
		{"role": "assistant", "content": []map[string]any{
			{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": "echo nudge"}},
		}},
		{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": id, "content": "nudge"},
			{"type": "text", "text": "<system-reminder>ctx</system-reminder>"},
		}},
	}
}

func envelopeWithMessages(t *testing.T, msgs []map[string]any) *translate.RequestEnvelope {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "claude-fable-5-1", "max_tokens": 10, "messages": msgs})
	require.NoError(t, err)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	return env
}

func TestTrailingRouterNudgeTurns(t *testing.T) {
	prompt := map[string]any{"role": "user", "content": "fix the bug"}
	realTool := []map[string]any{
		{"role": "assistant", "content": []map[string]any{
			{"type": "text", "text": "reading"},
			{"type": "tool_use", "id": "toolu_real", "name": "Read", "input": map[string]any{"file_path": "a.go"}},
		}},
		{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "toolu_real", "content": "pkg"}}},
	}

	cases := []struct {
		name string
		msgs []map[string]any
		want int
	}{
		{"fresh prompt", []map[string]any{prompt}, 0},
		{"real tool call last", append([]map[string]any{prompt}, realTool...), 0},
		{"two nudges after real tool", append(append(append([]map[string]any{prompt}, realTool...), nudgeTurn("toolu_router_nudge_a")...), nudgeTurn("toolu_router_nudge_b")...), 2},
		{"real tool after nudge resets", append(append([]map[string]any{prompt}, nudgeTurn("toolu_router_nudge_a")...), realTool...), 0},
		{"new user prompt resets", append(append([]map[string]any{prompt}, nudgeTurn("toolu_router_nudge_a")...), prompt), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, envelopeWithMessages(t, tc.msgs).TrailingRouterNudgeTurns())
		})
	}
}
