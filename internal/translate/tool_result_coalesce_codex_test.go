package translate_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/translate"
)

// Codex code-mode records each notify() from an exec script as another
// custom_tool_call_output under the running call's call_id, after the final
// result. Anthropic rejects more than one tool_result per tool_use.
func codexNotifyHistory(t *testing.T) *translate.RequestEnvelope {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5.6-sol",
		"input": []any{
			codexUserItem("poll the build until it finishes"),
			map[string]any{"type": "custom_tool_call", "call_id": "toolu_dup_1", "name": "exec", "input": "while (!done) { notify(status) }"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "toolu_dup_1", "output": []any{
				map[string]any{"type": "input_text", "text": "Script completed\nWall time 12.0 seconds\nOutput:\n"},
				map[string]any{"type": "input_text", "text": "build passed"},
			}},
			map[string]any{"type": "custom_tool_call_output", "call_id": "toolu_dup_1", "output": "status: queued"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "toolu_dup_1", "output": "status: running"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "toolu_dup_1", "output": "status: passed"},
			codexUserItem("thanks, what next?"),
		},
	})
	require.NoError(t, err)
	conv, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	env, err := translate.ParseOpenAI(conv.Body)
	require.NoError(t, err)
	return env
}

func TestCoalesceDuplicateToolResults_CodexNotifyHistoryEmitsOneAnthropicToolResult(t *testing.T) {
	env := codexNotifyHistory(t)

	assert.Equal(t, 3, env.CoalesceDuplicateToolResults())

	out, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5"})
	require.NoError(t, err)
	var results []gjson.Result
	gjson.GetBytes(out.Body, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_result" {
				results = append(results, block)
			}
			return true
		})
		return true
	})
	require.Len(t, results, 1, "Anthropic requires exactly one tool_result per tool_use")
	assert.Equal(t, "toolu_dup_1", results[0].Get("tool_use_id").String())

	var text strings.Builder
	content := results[0].Get("content")
	if content.Type == gjson.String {
		text.WriteString(content.String())
	} else {
		content.ForEach(func(_, part gjson.Result) bool {
			text.WriteString(part.Get("text").String())
			return true
		})
	}
	merged := text.String()
	last := -1
	for _, want := range []string{"Script completed", "build passed", "status: queued", "status: running", "status: passed"} {
		idx := strings.Index(merged, want)
		require.GreaterOrEqual(t, idx, 0, "merged result must keep %q", want)
		assert.Greater(t, idx, last, "outputs must stay in recorded order")
		last = idx
	}
}

func TestCoalesceDuplicateToolResults_CodexNotifyHistoryEmitsOneGeminiFunctionResponse(t *testing.T) {
	env := codexNotifyHistory(t)
	require.Positive(t, env.CoalesceDuplicateToolResults())

	out, err := env.PrepareGemini(nil, translate.EmitOptions{TargetModel: "gemini-2.5-pro"})
	require.NoError(t, err)
	calls, responses := 0, 0
	gjson.GetBytes(out.Body, "contents").ForEach(func(_, c gjson.Result) bool {
		c.Get("parts").ForEach(func(_, p gjson.Result) bool {
			if p.Get("functionCall").Exists() {
				calls++
			}
			if p.Get("functionResponse").Exists() {
				responses++
			}
			return true
		})
		return true
	})
	assert.Equal(t, 1, calls)
	assert.Equal(t, calls, responses, "Gemini requires one functionResponse per functionCall")
}
