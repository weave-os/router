package translate

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// Keep legacy branded markers recognizable without adding visible copy to
// existing notices. New prose carries the same invisible provenance prefix as
// terminal Responses badges, so replay cleanup does not depend on its wording.
const routerNoticeSentinel = "\u2063\u2060\u2063\u2060"
const legacyRoutingMarkerExpression = `✦ \*\*Weave Router\*\* → .*?\n\n`

// RescueDetailPrefix opens the paragraph a rescue badge appends to name the
// failed attempt; replay cleanup removes it together with the badge.
const RescueDetailPrefix = "↳ switched: "

// SlowStartPrefix opens the notice naming a provider that was slow to start.
const SlowStartPrefix = "↳ "

const rescueDetailExpression = `(?:` + RescueDetailPrefix + `[^\n]*\n\n)?`

const legacyNoticeExpression = `(?:\*\*WEAVE ROUTER\*\* — .*?\n\n|✦ \*\*WEAVE ROUTER\*\* → .*?\n\n)`

var routerOwnedNoticePattern = regexp.MustCompile(`(?s)(?:` + regexp.QuoteMeta(routerNoticeSentinel) + `.*?\n\n|` + legacyRoutingMarkerExpression + `)` + rescueDetailExpression)

func IsRouterNoticeText(text string) bool {
	return routerOwnedNoticePattern.MatchString(text)
}

// LastAnnouncedResponsesModel reads client-visible selection history before
// replay cleanup removes it. Provider completion history can lag an eager notice
// or belong to a concurrent agent sharing the caller's session identifier.
func LastAnnouncedResponsesModel(body []byte) string {
	items := gjson.GetBytes(body, "input").Array()
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if item.Get("role").Str != "assistant" || (item.Get("type").Str != "message" && item.Get("type").Str != "") {
			continue
		}
		content := item.Get("content")
		if content.Type == gjson.String {
			if model := announcedResponsesModel(content.Str); model != "" {
				return model
			}
		}
		for _, part := range content.Array() {
			switch part.Get("type").Str {
			case "input_text", "output_text", "text":
				if model := announcedResponsesModel(part.Get("text").Str); model != "" {
					return model
				}
			default:
				continue
			}
			// Replay cleanup trusts only the first text part, where egress puts notices.
			break
		}
	}
	return ""
}

func announcedResponsesModel(text string) string {
	model := ""
	for {
		span := responsesTerminalBadgePattern.FindStringIndex(text)
		if span == nil {
			return model
		}
		owned := strings.TrimPrefix(text[:span[1]], routerNoticeSentinel)
		for _, prefix := range []string{"✦ **Weave Router** → ", "**Weave Router** — "} {
			if selection, ok := strings.CutPrefix(owned, prefix); ok {
				line, _, _ := strings.Cut(selection, "\n")
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
