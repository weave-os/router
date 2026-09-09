package router

import "strings"

// UserTextIndex returns the latest actual textual instruction, excluding
// tool-only user messages, or -1 when classification evidence is unavailable.
func UserTextIndex(messages []ConversationMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(messages[i].Role), "user") && strings.TrimSpace(messages[i].Text) != "" {
			return i
		}
	}
	return -1
}
