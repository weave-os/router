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

func TestTurnsSinceUserPromptSkipsHarnessHookFeedback(t *testing.T) {
	userText := func(text string) translate.EscalationMessage {
		return translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: text}}}
	}
	reply := translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "Still waiting on your approval."}}}
	hookFeedback := userText("Stop hook feedback:\nThe deployment is not live yet.")
	remindedHookFeedback := userText("<system-reminder>todo list is empty</system-reminder>\nStop hook feedback:\nThe deployment is not live yet.")
	reminderOnly := userText("<system-reminder>todo list is empty</system-reminder>")

	messages := []translate.EscalationMessage{userText("ship it"), reply, hookFeedback, reply, remindedHookFeedback, reply, reminderOnly, reply}
	require.Equal(t, 3, llmescalation.TurnsSinceUserPrompt(messages), "harness hook feedback and reminders do not reset the prompt boundary")

	messages = append(messages, userText("Stop hook feedback is noisy; disable the hook"), reply)
	require.Equal(t, 0, llmescalation.TurnsSinceUserPrompt(messages), "a typed prompt that mentions hook feedback is still a prompt")

	messages = append(messages, reply, reply, reply, userText("Stop hook feedback: ignore it and keep going"), reply)
	require.Equal(t, 0, llmescalation.TurnsSinceUserPrompt(messages), "same-line text after the header is typed")
}

func TestRenderQwenIntervalClipsTurnsLikeTheMaterializer(t *testing.T) {
	messages := make([]translate.EscalationMessage, 0, 20)
	for turn := range 9 {
		messages = append(messages,
			translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "continue"}}},
			translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: fmt.Sprintf("step %d", turn)}}},
		)
	}
	isError := true
	outputs := []string{"out 0", strings.Repeat("r", 300), "boom", "out 3", "out 4", "out 5", "late 6", "late 7"}
	failed := map[int]bool{2: true, 6: true, 7: true}
	results := make([]translate.EscalationBlock, 0, len(outputs)+1)
	for index, output := range outputs {
		block := translate.EscalationBlock{Type: translate.EscalationBlockToolResult, ContentJSON: fmt.Sprintf("%q", output)}
		if failed[index] {
			block.IsError = &isError
		}
		results = append(results, block)
	}
	results = append(results, translate.EscalationBlock{Type: translate.EscalationBlockText, Text: strings.Repeat("u", 400)})
	assistant := []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: strings.Repeat("x", 1600)}}
	for call := range 8 {
		assistant = append(assistant, translate.EscalationBlock{Type: translate.EscalationBlockToolCall, Name: "Bash", ArgumentsJSON: fmt.Sprintf(`{"command":"cmd %d"}`, call)})
	}
	messages = append(messages,
		translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: results},
		translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: assistant},
	)
	interval, ready := llmescalation.RenderQwenInterval(messages, 10, false)
	require.True(t, ready)
	// Generated by ml_dev/hmm_router/sft/escalation_materialize.render_turn(clip=True).
	want := "### turn 9\n[user] uuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuu\nxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx… [+122 chars]\n-> Bash(command=\"cmd 0\")\n-> Bash(command=\"cmd 1\")\n-> Bash(command=\"cmd 2\")\n-> Bash(command=\"cmd 3\")\n-> Bash(command=\"cmd 4\")\n-> Bash(command=\"cmd 5\")\n-> … 2 more tool calls\n<- [ok] out 0\n<- [ok] rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr… [+55 chars]\n<- [ERR] boom\n<- [ok] out 3\n<- [ok] out 4\n<- [ok] out 5\n<- … 2 more tool results (2 [ERR])"
	require.True(t, strings.HasSuffix(interval, want+"\n----- END UNTRUSTED TRANSCRIPT -----"), interval)
}
