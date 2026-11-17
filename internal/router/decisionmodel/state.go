package decisionmodel

import (
	"strings"

	"weave-os/router/internal/router"
)

const (
	maxUserMessages  = 3
	maxAgentTurns    = 6
	maxObjectiveRune = 2000
	maxUserRune      = 1000
	maxAgentRune     = 500
)

type ToolCallStatus string

const (
	ToolCallOK      ToolCallStatus = "ok"
	ToolCallError   ToolCallStatus = "error"
	ToolCallPending ToolCallStatus = "pending"
)

type ToolCall struct {
	Name   string         `json:"name"`
	Status ToolCallStatus `json:"status"`
}

type AgentTurn struct {
	Text      string     `json:"text,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type State struct {
	Objective          string      `json:"objective"`
	RecentUserMessages []string    `json:"recent_user_messages"`
	RecentAgentTurns   []AgentTurn `json:"recent_agent_turns"`
}

func isHumanMessage(message router.ConversationMessage) bool {
	return message.Role == "user" && message.Text != ""
}

func BuildState(messages []router.ConversationMessage) State {
	state := State{RecentUserMessages: []string{}, RecentAgentTurns: []AgentTurn{}}
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		switch {
		case isHumanMessage(message) && state.Objective == "":
			state.Objective = truncate(message.Text, maxObjectiveRune)
		case isHumanMessage(message):
			state.RecentUserMessages = appendBounded(state.RecentUserMessages, truncate(message.Text, maxUserRune), maxUserMessages)
		case message.Role == "assistant":
			end := callEnd(messages, index)
			state.RecentAgentTurns = appendBounded(state.RecentAgentTurns, agentTurn(messages[index:end], messages[end:]), maxAgentTurns)
			index = end - 1
		}
	}
	return state
}

func callEnd(messages []router.ConversationMessage, start int) int {
	end := start
	for end < len(messages) && messages[end].Role == "assistant" {
		end++
	}
	return end
}

func agentTurn(call []router.ConversationMessage, following []router.ConversationMessage) AgentTurn {
	var texts []string
	var toolCalls []router.ConversationToolCall
	for _, message := range call {
		if message.Text != "" {
			texts = append(texts, message.Text)
		}
		toolCalls = append(toolCalls, message.ToolCalls...)
	}
	var results []router.ConversationToolResult
	for _, message := range following {
		if message.Role == "assistant" {
			break
		}
		results = append(results, message.ToolResults...)
		if isHumanMessage(message) {
			break
		}
	}
	turn := AgentTurn{Text: truncate(strings.Join(texts, "\n"), maxAgentRune)}
	for index, call := range toolCalls {
		status := ToolCallPending
		if index < len(results) && results[index].IsError {
			status = ToolCallError
		} else if index < len(results) {
			status = ToolCallOK
		}
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{Name: call.Name, Status: status})
	}
	return turn
}

func AgentCallIndex(messages []router.ConversationMessage) int {
	calls := 1
	for index := 0; index < len(messages); index++ {
		if messages[index].Role == "assistant" {
			calls++
			index = callEnd(messages, index) - 1
		}
	}
	return calls
}

func appendBounded[T any](items []T, item T, limit int) []T {
	items = append(items, item)
	if len(items) > limit {
		items = items[len(items)-limit:]
	}
	return items
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
