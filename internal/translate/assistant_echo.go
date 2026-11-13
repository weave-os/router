package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

// EchoedInAssistantText reports, per needle, whether any assistant text the
// client sent back (Chat/Messages `messages` or Responses `input`) contains
// it. Clients echo router-injected text verbatim, so this is how the router
// knows what a conversation has already shown. Run it on the original body,
// before routing-marker stripping erases the evidence.
func EchoedInAssistantText(body []byte, needles []string) map[string]bool {
	echoed := make(map[string]bool, len(needles))
	visit := func(text string) {
		for _, needle := range needles {
			if !echoed[needle] && strings.Contains(text, strings.TrimSpace(needle)) {
				echoed[needle] = true
			}
		}
	}
	for _, msg := range gjson.GetBytes(body, "messages").Array() {
		if msg.Get("role").Str == "assistant" {
			visit(assistantMessageText(msg))
		}
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("role").Str != "assistant" {
			continue
		}
		content := item.Get("content")
		if content.Type == gjson.String {
			visit(content.Str)
			continue
		}
		for _, part := range content.Array() {
			visit(part.Get("text").Str)
		}
	}
	return echoed
}
