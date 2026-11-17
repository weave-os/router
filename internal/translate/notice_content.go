package translate

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

const routerNoticeSentinel = "\u2063\u2060\u2063\u2060"
const legacyRoutingMarkerExpression = `✦ \*\*Weave Router\*\* → .*?\n\n`

const trimmedRoutingMarkerExpression = `\A(?:` + routerNoticeSentinel + `)?✦ \*\*Weave Router\*\* → [^\n]*\z`

const RescueDetailPrefix = "↳ switched: "

const SlowStartPrefix = "↳ "

const rescueDetailExpression = `(?:` + RescueDetailPrefix + `[^\n]*\n\n)?`

const legacyNoticeExpression = `(?:\*\*WEAVE ROUTER\*\* — .*?\n\n|✦ \*\*WEAVE ROUTER\*\* → .*?\n\n)`

var routerOwnedNoticePattern = regexp.MustCompile(`(?s)(?:` + regexp.QuoteMeta(routerNoticeSentinel) + `.*?\n\n|` + legacyRoutingMarkerExpression + `)` + rescueDetailExpression)

var replayedNoticePattern = regexp.MustCompile(`(?s)(?:` + regexp.QuoteMeta(routerNoticeSentinel) + `.*?\n\n|` + legacyRoutingMarkerExpression + `|` + trimmedRoutingMarkerExpression + `)` + rescueDetailExpression)

var replayedSlowStartPattern = regexp.MustCompile(`\A` + regexp.QuoteMeta(routerNoticeSentinel+SlowStartPrefix) + `[^\n]+ took [0-9]+s to first output \([^\n]+\)(?:\n\n|\z)`)

func SlowStartInResponsesTurn(body []byte) bool {
	items := gjson.GetBytes(body, "input").Array()
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		kind := escalationWireType(item.Get("type").Str)
		if kind != "" && kind != escalationWireMessage {
			continue
		}
		role := EscalationRole(item.Get("role").Str)
		if role != EscalationRoleAssistant && role != EscalationRoleUser {
			continue
		}
		blocks, media, err := escalationContentBlocks(item.Get("content"))
		if err != nil || (role == EscalationRoleUser && media) {
			return false
		}
		for _, block := range blocks {
			if block.Type != EscalationBlockText {
				continue
			}
			if role == EscalationRoleUser {
				if !isOnlyKnownInjectedText(block.Text) {
					return false
				}
				continue
			}
			text := block.Text
			for {
				if replayedSlowStartPattern.MatchString(text) {
					return true
				}
				span := replayedNoticePattern.FindStringIndex(text)
				if span == nil || span[0] != 0 {
					break
				}
				text = text[span[1]:]
			}
			break
		}
	}
	return false
}

func IsRouterNoticeText(text string) bool {
	return routerOwnedNoticePattern.MatchString(text)
}

func LastAnnouncedResponsesModel(body []byte) string {
	return lastAnnouncedResponsesModel(body, responsesTerminalBadgePattern)
}

func LastReplayedResponsesModel(body []byte) string {
	return lastAnnouncedResponsesModel(body, replayedAnnouncementPattern)
}

func lastAnnouncedResponsesModel(body []byte, pattern *regexp.Regexp) string {
	items := gjson.GetBytes(body, "input").Array()
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if item.Get("role").Str != "assistant" || (item.Get("type").Str != "message" && item.Get("type").Str != "") {
			continue
		}
		content := item.Get("content")
		if content.Type == gjson.String {
			if model := announcedModel(content.Str, pattern); model != "" {
				return model
			}
		}
		for _, part := range content.Array() {
			switch part.Get("type").Str {
			case "input_text", "output_text", "text":
				if model := announcedModel(part.Get("text").Str, pattern); model != "" {
					return model
				}
			default:
				continue
			}
			break
		}
	}
	return ""
}

var replayedAnnouncementPattern = regexp.MustCompile(`(?s)\A(?:` + regexp.QuoteMeta(routerNoticeSentinel) + `.*?\n\n|` + legacyRoutingMarkerExpression + `|` + trimmedRoutingMarkerExpression + `)` + rescueDetailExpression)

func LastAnnouncedMessagesModel(body []byte) string {
	messages := gjson.GetBytes(body, "messages").Array()
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Get("role").Str != "assistant" {
			continue
		}
		if model := announcedModel(firstAssistantText(messages[i].Get("content")), replayedAnnouncementPattern); model != "" {
			return model
		}
	}
	return ""
}

func firstAssistantText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.Str
	}
	for _, block := range content.Array() {
		if block.Get("type").Str == "text" {
			return block.Get("text").Str
		}
	}
	return ""
}

func announcedModel(text string, pattern *regexp.Regexp) string {
	model := ""
	for {
		span := pattern.FindStringIndex(text)
		if span == nil {
			return model
		}
		owned := strings.TrimPrefix(text[:span[1]], routerNoticeSentinel)
		for _, prefix := range []string{"✦ **Weave Router** → ", "**Weave Router** — "} {
			if selection, ok := strings.CutPrefix(owned, prefix); ok {
				line, _, _ := strings.Cut(selection, "\n")
				line = strings.TrimPrefix(line, "Delegating work with ")
				fields := strings.Fields(line)
				if len(fields) > 0 {
					model = fields[0]
				}
			}
		}
		text = text[span[1]:]
	}
}

func stripResponsesTerminalBadges(text string) string {
	for {
		span := responsesTerminalBadgePattern.FindStringIndex(text)
		if span == nil {
			return text
		}
		text = text[span[1]:]
	}
}

func noticeWireText(text string) string {
	if IsRouterNoticeText(text) {
		return text
	}
	return routerNoticeSentinel + strings.TrimSpace(text) + "\n\n"
}
