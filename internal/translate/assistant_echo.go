package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

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

func assistantMessageText(msg gjson.Result) string {
	content := msg.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var b strings.Builder
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != "text" {
			return true
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(block.Get("text").String())
		return true
	})
	return b.String()
}
