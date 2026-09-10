package proxy

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

// handleRouterModelsCommand answers a bare router-models directive
// synthetically, with no upstream call.
//
// This reports strictly more than the skill it replaces. The skill shells out
// to `weave-router models`, which on a managed router gets a 404 from the
// admin API and degrades to the unauthenticated catalog -- a flat list that
// has to tell the user "this router does not report which of them your
// installation has enabled". The exclusions ARE on this request, so the
// router can mark each row instead of disclaiming the question.
func (s *Service) handleRouterModelsCommand(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	inputTokens int,
) error {
	log := observability.FromContext(ctx)
	routable := s.RoutableModels()
	excluded := s.excludedModelsForRequest(ctx)
	// Deployment-wide automatic exclusions are a separate, softer set: the
	// router may not CHOOSE these, but an explicit force still serves them.
	// Folding them into `excluded` would claim they are unusable; leaving
	// them out would claim they are routable. They get their own marker.
	automaticExcluded := s.globalAutomaticExcludedModels(ctx)

	log.Debug("/router-models: answered from the request's own routing view",
		"routable", len(routable),
		"excluded", len(excluded),
		"automatic_excluded", len(automaticExcluded),
	)

	msg := routerModelsMessage(routable, excluded, automaticExcluded,
		directivePrefix(ClientIdentityFrom(ctx).ClientApp), env.SourceFormat())
	if env.SourceFormat() == translate.FormatOpenAI {
		return writeSyntheticOpenAIResponse(w, env, msg, inputTokens)
	}
	return writeSyntheticAnthropicResponse(w, env, msg, inputTokens)
}

// routerModelsMessage renders the routable universe grouped by each model's
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
func routerModelsMessage(routable, excluded, automaticExcluded map[string]struct{}, prefix string, format translate.Format) string {
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
