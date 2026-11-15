package conversation

import (
	"fmt"
	"sort"
	"strings"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

// RouterSessionMessage renders the ack in the marker style each surface uses:
// OpenAI-format clients (Codex) get plain text, Anthropic-format clients get
// the ✦ marker the rest of the router's synthetic replies use there.
func RouterSessionMessage(sessionID, prefix string, format translate.Format) string {
	if sessionID == "" {
		// No session header means nothing here is correlated, so naming a
		// substitute id would point support at the wrong session. Say so.
		const detail = "no session id on this request. This client did not send one, so its turns are not correlated in router telemetry."
		if format == translate.FormatOpenAI {
			return "Weave Router: " + detail
		}
		return "✦ **Weave Router** → " + detail + "\n\n"
	}
	hint := prefix + "router-feedback"
	if format == translate.FormatOpenAI {
		return fmt.Sprintf("Weave Router: session id: %s. Quote it in support requests; %s attaches feedback to this session.", sessionID, hint)
	}
	return fmt.Sprintf("✦ **Weave Router** → session id: `%s` · quote it in support requests; `%s` attaches feedback to this session\n\n", sessionID, hint)
}

// RouterModelsMessage renders the routable universe grouped by each model's
// primary provider, marking what this installation may actually be routed to.
// The [x]/[ ] form matches what `weave-router models` prints, so the two
// surfaces stay recognizably the same listing.
//
// [-] is the third state, and it exists because the two model-restriction
// layers differ in kind: [ ] is an org exclusion or a model outside the
// allowlist, which nothing can route to, while [-] is a deployment-wide
// automatic exclusion the scorer honours but an explicit force ignores. The
// legend is printed only when at least one model is in that state, so the
// common listing is unchanged.
func RouterModelsMessage(routable, excluded, automaticExcluded map[string]struct{}, prefix string, format translate.Format) string {
	if len(routable) == 0 {
		const detail = "no routable models are configured on this router."
		if format == translate.FormatOpenAI {
			return "Weave Router: " + detail
		}
		return "✦ **Weave Router** → " + detail + "\n\n"
	}

	byProvider := make(map[string][]string, len(routable))
	for id := range routable {
		provider := "other"
		if m, ok := catalog.ByID(id); ok {
			if p := m.PrimaryProvider(); p != "" {
				provider = p
			}
		}
		byProvider[provider] = append(byProvider[provider], id)
	}
	providers := make([]string, 0, len(byProvider))
	for p := range byProvider {
		providers = append(providers, p)
	}
	sort.Strings(providers)

	var b strings.Builder
	enabled := 0
	forceOnly := 0
	for _, p := range providers {
		models := byProvider[p]
		sort.Strings(models)
		b.WriteString("\n" + p + "\n")
		for _, id := range models {
			mark := "x"
			switch {
			case contains(excluded, id):
				mark = " "
			case contains(automaticExcluded, id):
				mark = "-"
				forceOnly++
			default:
				enabled++
			}
			b.WriteString(fmt.Sprintf("  [%s] %s\n", mark, id))
		}
	}

	// The mutating subcommands stay on the skill (they need admin auth the
	// chat key deliberately lacks), so the footer has to name the real path
	// rather than imply this directive can change anything.
	footer := fmt.Sprintf(
		"\n%d of %d routable here. Change the selection with `%srouter-models enable <id>` or `%srouter-models disable <id>`.",
		enabled, len(routable), prefix, prefix)
	if forceOnly > 0 {
		footer += fmt.Sprintf(
			"\n[-] means automatic routing is disabled for that model deployment-wide; %sforce-model still serves it.",
			prefix)
	}

	if format == translate.FormatOpenAI {
		return fmt.Sprintf("Weave Router: models this installation can be routed to.\n%s%s", b.String(), footer)
	}
	return fmt.Sprintf("✦ **Weave Router** → models this installation can be routed to\n```\n%s```\n%s\n\n", b.String(), footer)
}

// contains keeps the three-way mark switch readable; a nil map is empty.
func contains(set map[string]struct{}, id string) bool {
	_, ok := set[id]
	return ok
}

// RouterFeedbackAck renders the acknowledgment, echoing the verdict. The
// Anthropic-format ack is wrapped as a routing marker so it gets stripped
// from subsequent turns.
func RouterFeedbackAck(format translate.Format, rating string) string {
	verdict := ""
	switch rating {
	case translate.RouterFeedbackRatingUp:
		verdict = " 👍"
	case translate.RouterFeedbackRatingDown:
		verdict = " 👎"
	}
	if format == translate.FormatOpenAI {
		return "Weave Router: Feedback recorded" + verdict + ". Thank you."
	}
	return "✦ **Weave Router** → Feedback recorded" + verdict + ". Thank you.\n\n"
}
