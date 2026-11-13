package translate

import (
	"net/http"
	"strings"
)

// Message-thread request types Claude Code sends in the top-level `thread`
// field. A create carries the full transcript; a continue carries only the
// messages after previous_message_id and may omit unchanged system/tools.
const (
	MessageThreadCreate   = "create"
	MessageThreadContinue = "continue"
)

const messageThreadsBetaPrefix = "message-threads-"

// StripMessageThreadsBeta removes message-threads tokens from anthropic-beta.
func StripMessageThreadsBeta(h http.Header) {
	values := h.Values("anthropic-beta")
	if len(values) == 0 {
		return
	}
	kept := joinKept(strings.Join(values, ","), func(token string) bool {
		return !strings.HasPrefix(token, messageThreadsBetaPrefix)
	})
	if kept == "" {
		h.Del("anthropic-beta")
		return
	}
	h.Set("anthropic-beta", kept)
}
