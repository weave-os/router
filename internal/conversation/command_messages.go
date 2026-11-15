package conversation

import (
	"fmt"
	"sort"
	"strings"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

func RouterSessionMessage(sessionID, prefix string, format translate.Format) string {
	if sessionID == "" {
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
			fmt.Fprintf(&b, "  [%s] %s\n", mark, id)
		}
	}

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

func contains(set map[string]struct{}, id string) bool {
	_, ok := set[id]
	return ok
}

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
