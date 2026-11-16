package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

func (e *RequestEnvelope) TrailingAssistantTexts() []string {
	switch e.format {
	case FormatAnthropic, FormatOpenAI:
	default:
		return nil
	}
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return nil
	}
	all := msgs.Array()
	var rev []string
loop:
	for i := len(all) - 1; i >= 0; i-- {
		msg := all[i]
		switch msg.Get("role").String() {
		case "user":
			if userIsHumanTurn(msg) {
				break loop
			}
		case "assistant":
			if s := assistantMessageText(msg); s != "" {
				rev = append(rev, s)
			}
		}
	}
	for l, r := 0, len(rev)-1; l < r; l, r = l+1, r-1 {
		rev[l], rev[r] = rev[r], rev[l]
	}
	return rev
}

func userIsHumanTurn(msg gjson.Result) bool {
	content := msg.Get("content")
	if content.Type == gjson.String {
		return isHumanText(content.String())
	}
	if !content.IsArray() {
		return false
	}
	found := false
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != "text" {
			return true
		}
		if isHumanText(block.Get("text").String()) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isHumanText(s string) bool {
	return strings.TrimSpace(s) != "" && !isClaudeCodeInjectedBlock(s)
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
