package translate

import "strings"

// RoutingHistoryProfile selects native client lifecycle semantics.
type RoutingHistoryProfile uint8

const (
	RoutingHistoryDefault RoutingHistoryProfile = iota
	RoutingHistoryClaudeCode
	RoutingHistoryOpenCode
)

func (e *RequestEnvelope) routingMessages(profile RoutingHistoryProfile) ([]ConversationMessage, bool) {
	if profile == RoutingHistoryClaudeCode {
		return e.ClaudeCodeRoutingMessages()
	}
	messages := e.ConversationMessages()
	projected := profile == RoutingHistoryOpenCode && projectOpenCodeBackgroundCompletions(messages)
	return messages, projected
}

// RoutingInput projects client lifecycle messages without changing provider input.
func (e *RequestEnvelope) RoutingInput(profile RoutingHistoryProfile, promptText string, onlyUser bool) (string, []ConversationMessage) {
	messages, projected := e.routingMessages(profile)
	if projected && onlyUser {
		var userText []string
		for _, message := range messages {
			if message.Role == string(EscalationRoleUser) && message.Text != "" {
				userText = append(userText, message.Text)
			}
		}
		promptText = strings.Join(userText, "\n")
	}
	return promptText, messages
}

// RoutingEndsWithUserPrompt excludes correlated background completions from user boundaries.
func (e *RequestEnvelope) RoutingEndsWithUserPrompt(profile RoutingHistoryProfile) bool {
	if profile == RoutingHistoryOpenCode {
		messages, projected := e.routingMessages(profile)
		if projected {
			for index := len(messages) - 1; index >= 0; index-- {
				last := messages[index]
				if last.Role == string(EscalationRoleSystem) || last.Role == string(EscalationRoleDeveloper) {
					continue
				}
				if _, completion := openCodeBackgroundCompletion(last.Text); last.Role == string(EscalationRoleAssistant) && completion {
					return false
				}
				break
			}
		}
	}
	return e.EndsWithUserPrompt()
}
