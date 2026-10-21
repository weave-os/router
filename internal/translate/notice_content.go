package translate

import (
	"regexp"
	"strings"
)

// Keep legacy branded markers recognizable without adding visible copy to
// existing notices. New prose carries the same invisible provenance prefix as
// terminal Responses badges, so replay cleanup does not depend on its wording.
const routerNoticeSentinel = "\u2063\u2060\u2063\u2060"
const legacyRoutingMarkerExpression = `✦ \*\*Weave Router\*\* → .*?\n\n`
const legacyNoticeExpression = `(?:\*\*WEAVE ROUTER\*\* — .*?\n\n|✦ \*\*WEAVE ROUTER\*\* → .*?\n\n)`

var routerOwnedNoticePattern = regexp.MustCompile(`(?s)(?:` + regexp.QuoteMeta(routerNoticeSentinel) + `.*?\n\n|` + legacyRoutingMarkerExpression + `)`)

func IsRouterNoticeText(text string) bool {
	return routerOwnedNoticePattern.MatchString(text)
}

func noticeWireText(text string) string {
	if IsRouterNoticeText(text) {
		return text
	}
	return routerNoticeSentinel + strings.TrimSpace(text) + "\n\n"
}
