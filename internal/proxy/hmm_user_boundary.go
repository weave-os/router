package proxy

import (
	"strings"

	"weave-os/router/internal/router"
)

func hasTextUserBoundary(messages []router.ConversationMessage) bool {
	for _, message := range messages {
		if strings.EqualFold(message.Role, "user") && strings.TrimSpace(message.Text) != "" {
			return true
		}
	}
	return false
}
