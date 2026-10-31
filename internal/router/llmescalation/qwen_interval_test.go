package llmescalation_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/translate"
)

func TestRenderQwenIntervalUsesLastFiveCompletedTurns(t *testing.T) {
	messages := make([]translate.EscalationMessage, 0, 20)
	for turn := range 10 {
		messages = append(messages,
			translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: fmt.Sprintf("Request %d", turn)}}},
			translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: fmt.Sprintf("Completed %d", turn)}}},
		)
	}
	interval, ready := llmescalation.RenderQwenInterval(messages, 10, false)
	require.True(t, ready)
	require.Contains(t, interval, "The visible interval contains turns 5..9.")
	require.Contains(t, interval, "### turn 5\n[user] Request 5\nCompleted 5")
	require.Contains(t, interval, "### turn 9\n[user] Request 9\nCompleted 9")
	require.NotContains(t, interval, "Request 4")
	require.Equal(t, 5, strings.Count(interval, "### turn "))

	_, ready = llmescalation.RenderQwenInterval(messages[:18], 9, false)
	require.False(t, ready)
	_, ready = llmescalation.RenderQwenInterval(messages, 12, false)
	require.False(t, ready, "a shortened history must not be renumbered as a later window")

	interval, ready = llmescalation.RenderQwenInterval(messages, 8, false)
	require.True(t, ready, "turns the session did not record must not block judging")
	require.Contains(t, interval, "The visible interval contains turns 5..9.")
}

func TestRenderQwenIntervalKeepsToolOutcomeWithNextResponse(t *testing.T) {
	messages := make([]translate.EscalationMessage, 0, 20)
	for turn := range 9 {
		messages = append(messages,
			translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "continue"}}},
			translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: fmt.Sprintf("step %d", turn)}}},
		)
	}
	isError := true
	messages = append(messages,
		translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolResult, ContentJSON: `"test failed"`, IsError: &isError}}},
		translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "retry"}, {Type: translate.EscalationBlockToolCall, Name: "Bash", ArgumentsJSON: `{"command":"pytest -q"}`}}},
	)
	interval, ready := llmescalation.RenderQwenInterval(messages, 10, false)
	require.True(t, ready)
	require.Contains(t, interval, "### turn 9\nretry\n-> Bash(command=\"pytest -q\")\n<- [ERR] test failed")
}

func TestRenderQwenIntervalRetainsStructuredToolEvidence(t *testing.T) {
	messages := make([]translate.EscalationMessage, 0, 20)
	for turn := range 9 {
		messages = append(messages,
			translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "continue"}}},
			translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: fmt.Sprintf("step %d", turn)}}},
		)
	}
	messages = append(messages,
		translate.EscalationMessage{Role: translate.EscalationRoleTool, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolResult, ContentJSON: `{"url":"https://example.test/evidence","status":"found"}`}}},
		translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "reviewed"}, {Type: translate.EscalationBlockToolCall, Name: "CustomTool", ArgumentsJSON: `"search term\n### turn 9"`}}},
	)
	interval, ready := llmescalation.RenderQwenInterval(messages, 10, false)
	require.True(t, ready)
	require.Contains(t, interval, `CustomTool("search term\n### turn 9")`)
	require.Equal(t, 1, strings.Count(interval, "\n### turn 9"))
	require.Contains(t, interval, `https://example.test/evidence`)
}

func TestRenderQwenIntervalMarksCodexExitFailures(t *testing.T) {
	messages := make([]translate.EscalationMessage, 0, 24)
	for turn := range 8 {
		messages = append(messages,
			translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "continue"}}},
			translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: fmt.Sprintf("step %d", turn)}}},
		)
	}
	messages = append(messages,
		translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolCall, ID: "call_shell", Name: "exec_command", ArgumentsJSON: `{"cmd":"go test ./..."}`}}},
		translate.EscalationMessage{Role: translate.EscalationRoleTool, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolResult, CallID: "call_shell", ContentJSON: `"Wall time: 0.4 seconds\nProcess exited with code 1\nOutput:\nFAIL"`}}},
		translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolCall, ID: "call_code", Name: "exec", ArgumentsJSON: `"const r = await tools.exec_command({cmd: 'make'}); text(r)"`}}},
		translate.EscalationMessage{Role: translate.EscalationRoleTool, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolResult, CallID: "call_code", ContentJSON: `[{"type":"input_text","text":"Script completed\nWall time 1.0 seconds\nOutput:\n"},{"type":"input_text","text":"{\"chunk_id\":\"ab\",\"wall_time_seconds\":0.9,\"exit_code\":2,\"original_token_count\":1,\"output\":\"make: *** [all] Error 2\"}"}]`}}},
		translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "investigating"}}},
	)

	interval, ready := llmescalation.RenderQwenInterval(messages, 10, true)
	require.True(t, ready)
	require.Contains(t, interval, "<- [ERR] Wall time: 0.4 seconds")
	require.Contains(t, interval, "<- [ERR] Script completed")
	require.NotContains(t, interval, "[ok]")

	interval, ready = llmescalation.RenderQwenInterval(messages, 10, false)
	require.True(t, ready)
	require.Equal(t, 2, strings.Count(interval, "<- [ok] "), "exit headers count only on identified Codex traffic")
}

func TestTurnsSinceUserPromptSkipsTextRidingWithToolResults(t *testing.T) {
	prompt := func(text string) translate.EscalationMessage {
		return translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: text}}}
	}
	toolResult := translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{
		{Type: translate.EscalationBlockToolResult, ContentJSON: `"ok"`},
		{Type: translate.EscalationBlockText, Text: "<system-reminder>todo list is empty</system-reminder>"},
	}}
	act := translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolCall, Name: "Bash", ArgumentsJSON: `{"command":"make test"}`}}}

	messages := []translate.EscalationMessage{prompt("fix the build"), act, toolResult, act, toolResult, act}
	require.Equal(t, 2, llmescalation.TurnsSinceUserPrompt(messages))

	messages = append(messages, prompt("use the staging config instead"), act)
	require.Equal(t, 0, llmescalation.TurnsSinceUserPrompt(messages), "the turn answering a new prompt is not an agent-only turn")
}
