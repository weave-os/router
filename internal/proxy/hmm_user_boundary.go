package proxy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/routingpolicy"
)

func hasTextUserBoundary(messages []router.ConversationMessage) bool {
	return routingpolicy.HasTextUserBoundary(messages)
}
