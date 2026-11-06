package proxy

import (
	"strings"

	"weave-os/router/internal/translate"
)

type openCodeCompletionState string
type openCodeResultTag string
type openCodeBackgroundTool string

const (
	openCodeRunning           openCodeCompletionState = "running"
	openCodeCompleted         openCodeCompletionState = "completed"
	openCodeError             openCodeCompletionState = "error"
	openCodeCancelled         openCodeCompletionState = "cancelled"
	openCodeTaskResult        openCodeResultTag       = "task_result"
	openCodeTaskError         openCodeResultTag       = "task_error"
	openCodeTaskTool          openCodeBackgroundTool  = "task"
	openCodeSubagentTool      openCodeBackgroundTool  = "subagent"
	openCodeSessionPrefix                             = "ses_"
	openCodeBackgroundReceipt                         = "The subagent is working in the background (sessionID: "
)

// OpenCode drops synthetic-message metadata before inference. A completion must
// match a prior background-task tool receipt before its routing role changes.
func openCodeBackgroundCompletion(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if id, state, ok := openCodeTaskOutput(text); ok {
		return id, state == openCodeCompleted || state == openCodeError
	}
	if rest, ok := strings.CutPrefix(text, `<subagent sessionID="`); ok {
		id, rest, ok := strings.Cut(rest, `" state="`)
		if !ok || !validOpenCodeCompletionSession(id) {
			return "", false
		}
		state, rest, ok := strings.Cut(rest, `" description="`)
		if !ok {
			return "", false
		}
		switch openCodeCompletionState(state) {
		case openCodeCompleted, openCodeError, openCodeCancelled:
		default:
			return "", false
		}
		_, _, ok = strings.Cut(rest, "\">\n")
		return id, ok && strings.HasSuffix(rest, "\n</subagent>")
	}
	return "", false
}

func openCodeTaskOutput(text string) (string, openCodeCompletionState, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), `<task id="`)
	if !ok {
		return "", "", false
	}
	id, rest, ok := strings.Cut(rest, `" state="`)
	if !ok || !validOpenCodeCompletionSession(id) {
		return "", "", false
	}
	stateText, rest, ok := strings.Cut(rest, "\">\n")
	if !ok {
		return "", "", false
	}
	state := openCodeCompletionState(stateText)
	var resultTag openCodeResultTag
	switch state {
	case openCodeRunning, openCodeCompleted:
		resultTag = openCodeTaskResult
	case openCodeError:
		resultTag = openCodeTaskError
	default:
		return "", "", false
	}
	if strings.HasPrefix(rest, "<summary>") {
		_, rest, ok = strings.Cut(rest, "</summary>\n")
		if !ok {
			return "", "", false
		}
	}
	valid := strings.HasPrefix(rest, "<"+string(resultTag)+">\n") && strings.HasSuffix(rest, "\n</"+string(resultTag)+">\n</task>")
	return id, state, valid
}

func openCodeSubagentReceipt(text string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), openCodeBackgroundReceipt)
	if !ok {
		return "", false
	}
	id, rest, ok := strings.Cut(rest, "). You will be notified automatically when it finishes.")
	return id, ok && validOpenCodeCompletionSession(id) && (rest == "" || strings.HasPrefix(rest, "\n"))
}

func validOpenCodeCompletionSession(id string) bool {
	if !strings.HasPrefix(id, openCodeSessionPrefix) || len(id) == len(openCodeSessionPrefix) || len(id) > MaxClientIdentifierLen {
		return false
	}
	for _, char := range id[len(openCodeSessionPrefix):] {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func projectOpenCodeBackgroundCompletions(messages []translate.ConversationMessage) bool {
	changed := false
	toolCalls := make(map[string]openCodeBackgroundTool)
	started := make(map[string]struct{})
	for index := range messages {
		message := &messages[index]
		for _, call := range message.ToolCalls {
			if call.ToolUseID != "" {
				toolCalls[call.ToolUseID] = openCodeBackgroundTool(call.Name)
			}
		}
		for _, result := range message.ToolResults {
			if result.IsError {
				continue
			}
			switch toolCalls[result.ToolUseID] {
			case openCodeTaskTool:
				id, state, ok := openCodeTaskOutput(result.Text)
				if ok && state == openCodeRunning {
					started[id] = struct{}{}
				}
			case openCodeSubagentTool:
				if id, ok := openCodeSubagentReceipt(result.Text); ok {
					started[id] = struct{}{}
				}
			}
		}
		if message.Role != string(translate.EscalationRoleUser) || len(message.ToolCalls) > 0 || len(message.ToolResults) > 0 {
			continue
		}
		id, completion := openCodeBackgroundCompletion(message.Text)
		if _, found := started[id]; !completion || !found {
			continue
		}
		delete(started, id)
		message.Role = string(translate.EscalationRoleAssistant)
		changed = true
	}
	return changed
}
