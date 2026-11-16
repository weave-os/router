package translate

import (
	"net/http"
	"strings"
)

const (
	MessageThreadCreate   = "create"
	MessageThreadContinue = "continue"
)

const messageThreadsBetaPrefix = "message-threads-"

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
