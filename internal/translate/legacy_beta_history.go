package translate

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

func (e *RequestEnvelope) StripRetiredBetaHistory() int {
	switch e.format {
	case FormatAnthropic, FormatOpenAI:
	default:
		return 0
	}
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}

	lastUserIdx := -1
	msgs.ForEach(func(key, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			lastUserIdx = int(key.Int())
		}
		return true
	})

	messages := msgs.Array()
	removed := 0
	rebuilt := make([]string, 0, len(messages))
	for idx := 0; idx < len(messages); idx++ {
		msg := messages[idx]
		if !carriesToolCall(msg) && betaReplyOnlyContent(msg, isBetaAckText) {
			removed++
			continue
		}
		if msg.Get("role").String() == "user" && idx != lastUserIdx && idx+1 < len(messages) &&
			isBetaCommandOnlyContent(msg.Get("content")) {
			if reply := messages[idx+1]; reply.Get("role").String() == "assistant" && !carriesToolCall(reply) &&
				(betaReplyOnlyContent(reply, isRetiredBetaReplyText) || isEmptyTextContent(reply.Get("content"))) {
				removed += 2
				idx++
				continue
			}
		}
		rebuilt = append(rebuilt, msg.Raw)
	}
	if removed == 0 {
		return 0
	}
	return e.setMessages(rebuilt, removed)
}

func isEmptyTextContent(content gjson.Result) bool {
	switch {
	case content.Type == gjson.String:
		return strings.TrimSpace(content.String()) == ""
	case content.Type == gjson.JSON && content.IsArray():
		empty := true
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "text" || strings.TrimSpace(block.Get("text").String()) != "" {
				empty = false
				return false
			}
			return true
		})
		return empty
	default:
		return false
	}
}

func parseLegacyBetaCommand(text string) (struct{}, bool, string) {
	prefixEnd := leadingInjectedPrefixEnd(text)
	prefix := text[:prefixEnd]
	lines := strings.Split(text[prefixEnd:], "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] != "/beta" && fields[0] != "/beta-llm" {
			return struct{}{}, false, text
		}
		remaining := append(append([]string{}, lines[:i]...), lines[i+1:]...)
		return struct{}{}, true, strings.TrimSpace(prefix + strings.Join(remaining, "\n"))
	}
	return struct{}{}, false, text
}

func isBetaCommandOnlyContent(content gjson.Result) bool {
	return commandOnlyContent(content, parseLegacyBetaCommand)
}

func betaReplyOnlyContent(msg gjson.Result, isReply func(string) bool) bool {
	return msg.Get("role").String() == "assistant" && syntheticTextOnlyContent(msg.Get("content"), isReply)
}

var retiredRoutingModelReply = regexp.MustCompile("^✦ \\*\\*Weave Router\\*\\* → Select a router with " +
	"(`wv router set-model [A-Za-z0-9._-]+`, )+or `wv router set-model [A-Za-z0-9._-]+`, then start a new session\\.$")

func isRetiredBetaReplyText(text string) bool {
	trimmed := strings.TrimSpace(text)
	return isBetaAckText(text) ||
		trimmed == "✦ **Weave Router** → Beta is always on for this installation; /beta has no effect." ||
		retiredRoutingModelReply.MatchString(trimmed)
}

func isBetaAckText(text string) bool {
	switch strings.TrimSpace(text) {
	case "✦ **Weave Router** → Beta enabled. Type /beta again to turn it off.",
		"✦ **Weave Router** → Qwen beta enabled. Type /beta-llm again to turn it off.",
		"✦ **Weave Router** → Qwen beta disabled. Stable routing restored.",
		"✦ **Weave Router** → Qwen beta is unavailable for this session.",
		"✦ **Weave Router** → Usage: /beta-llm",
		"✦ **Weave Router** → Beta has been retired. Internal routing requires authenticated account enrollment.",
		"✦ **Weave Router** → Beta disabled. Stable routing restored.",
		"✦ **Weave Router** → Beta is unavailable for this session.",
		"✦ **Weave Router** → Usage: /beta":
		return true
	default:
		return false
	}
}

func (e *RequestEnvelope) StripRetiredBetaNotices() int {
	switch e.format {
	case FormatAnthropic, FormatOpenAI:
	default:
		return 0
	}
	messages := gjson.GetBytes(e.body, "messages").Array()
	removed := 0
	rebuilt := make([]string, 0, len(messages))
	for idx := 0; idx < len(messages); idx++ {
		msg := messages[idx]
		rebuilt = append(rebuilt, msg.Raw)
		content := msg.Get("content")
		if idx+1 < len(messages) && msg.Get("role").String() == "user" &&
			!isBetaCommandOnlyContent(content) && hasLeadingBetaCommand(content) {
			if reply := messages[idx+1]; !carriesToolCall(reply) && betaReplyOnlyContent(reply, isRetiredBetaReplyText) {
				removed++
				idx++
			}
		}
	}
	if removed == 0 {
		return 0
	}
	return e.setMessages(rebuilt, removed)
}

func hasLeadingBetaCommand(content gjson.Result) bool {
	switch {
	case content.Type == gjson.String:
		_, found, _ := parseInCursorQuery(content.String(), parseLegacyBetaCommand)
		return found
	case content.Type == gjson.JSON && content.IsArray():
		found := false
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "text" {
				_, found, _ = parseInCursorQuery(block.Get("text").String(), parseLegacyBetaCommand)
			}
			return !found
		})
		return found
	default:
		return false
	}
}
