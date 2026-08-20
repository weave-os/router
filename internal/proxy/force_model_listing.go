package proxy

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"workweave/router/internal/router/catalog"
	"workweave/router/internal/translate"
)

// forceModelListEntry is one pinnable model in the /force-model listing.
type forceModelListEntry struct {
	// ID is the canonical catalog ID — the slug a user types to pin it.
	ID string
	// Provider is the binding the pin would actually resolve to, which is not
	// always the model's primary (forcedModelBinding walks past an excluded
	// primary to a permitted fallback).
	Provider string
	// Routable distinguishes automatic routing targets from passthrough-only
	// models. Both are pinnable; only the former can be chosen automatically.
	Routable bool
	// Aliases are the shorthands that resolve to this model, shortest first.
	Aliases []string
}

// pinnableModels returns every model this installation can pin, derived from
// the same gate (forcedModelBinding) that admits the pin — so it can't
// advertise a model that would be refused. Untiered (passthrough-only) rows
// are included: omitting them hides models a user can actually reach.
func (s *Service) pinnableModels(ctx context.Context) []forceModelListEntry {
	aliases := aliasesByCanonicalID()

	out := make([]forceModelListEntry, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		if len(m.Providers) == 0 {
			continue
		}
		binding, reason := s.forcedModelBinding(ctx, m.ID, m.Providers[0].Provider)
		if reason != "" {
			continue
		}
		if !s.providerRegistered(binding) {
			continue
		}
		out = append(out, forceModelListEntry{
			ID:       m.ID,
			Provider: binding,
			Routable: s.routableTarget(m),
			Aliases:  aliases[m.ID],
		})
	}

	// Routable first, then by ID: the models automatic routing can pick are
	// the ones a user is usually reaching for, and burying them under retired
	// passthrough rows is what a plain catalog-order dump would do.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Routable != out[j].Routable {
			return out[i].Routable
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// routableTarget reports whether automatic routing can select the model here:
// it needs a tier (untiered catalog rows are passthrough-only by definition)
// and, when the deployment declares its routing targets, membership in them.
func (s *Service) routableTarget(m catalog.Model) bool {
	if m.Tier == catalog.TierUnknown {
		return false
	}
	if s.availableModels == nil {
		return true
	}
	_, ok := s.availableModels[m.ID]
	return ok
}

// providerRegistered reports whether this deployment has a client for the
// provider. Listing a model whose provider isn't wired would advertise a pin
// that dispatches nowhere.
func (s *Service) providerRegistered(provider string) bool {
	if s.providers == nil {
		return true
	}
	_, ok := s.providers[provider]
	return ok
}

// aliasesByCanonicalID inverts forceModelAliases, sorting each model's
// shorthands shortest-first so the listing leads with the one worth typing.
func aliasesByCanonicalID() map[string][]string {
	out := make(map[string][]string, len(forceModelAliases))
	for alias, target := range forceModelAliases {
		out[target] = append(out[target], alias)
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool {
			if len(out[id][i]) != len(out[id][j]) {
				return len(out[id][i]) < len(out[id][j])
			}
			return out[id][i] < out[id][j]
		})
	}
	return out
}

// maxListedAliases caps the shorthands shown per model. Some models carry
// eight; printing them all turns the listing into a wall the user won't read.
const maxListedAliases = 3

// renderForceModelListing formats the pinnable-model listing as the routing
// marker body. Markdown for the Anthropic surface, plain text for OpenAI,
// matching how every other synthetic ack in this package splits.
func renderForceModelListing(entries []forceModelListEntry, format translate.Format) string {
	if len(entries) == 0 {
		const empty = "force-model: no models are available to pin on this installation. Check the installation's allowed/excluded model settings."
		if format == translate.FormatOpenAI {
			return "Weave Router: " + empty
		}
		return "✦ **Weave Router** → " + empty + "\n\n"
	}

	var b strings.Builder
	if format == translate.FormatOpenAI {
		b.WriteString("Weave Router: models you can pin with /force-model <id>.\n")
	} else {
		b.WriteString("✦ **Weave Router** → force-model: pick a model by id, e.g. `/force-model ")
		b.WriteString(entries[0].ID)
		b.WriteString("`\n")
	}

	routableCount := 0
	for _, e := range entries {
		if e.Routable {
			routableCount++
		}
	}

	writeSection := func(heading string, want bool) {
		first := true
		for _, e := range entries {
			if e.Routable != want {
				continue
			}
			if first {
				first = false
				if format == translate.FormatOpenAI {
					b.WriteString("\n" + heading + "\n")
				} else {
					b.WriteString("\n**" + heading + "**\n\n")
				}
			}
			b.WriteString(formatListEntry(e, format))
		}
	}

	writeSection("Routing targets", true)
	if routableCount < len(entries) {
		writeSection("Passthrough only (pinnable, never auto-selected)", false)
	}

	if format == translate.FormatOpenAI {
		b.WriteString("\nUse /unforce-model to clear a pin.")
		return b.String()
	}
	b.WriteString("\nUse `/unforce-model` to clear a pin.\n\n")
	return b.String()
}

// formatListEntry renders one model row with its provider and top aliases.
func formatListEntry(e forceModelListEntry, format translate.Format) string {
	aliases := e.Aliases
	if len(aliases) > maxListedAliases {
		aliases = aliases[:maxListedAliases]
	}
	suffix := ""
	if len(aliases) > 0 {
		suffix = " — also: " + strings.Join(aliases, ", ")
	}
	if format == translate.FormatOpenAI {
		return fmt.Sprintf("  %s (%s)%s\n", e.ID, e.Provider, suffix)
	}
	return fmt.Sprintf("- `%s` (%s)%s\n", e.ID, e.Provider, suffix)
}

// maxSuggestions caps the near-miss ids offered on an unrecognized model.
const maxSuggestions = 5

// renderForceModelRejection formats the unrecognized-model reply, offering the
// closest pinnable ids instead of three hardcoded examples that may not even
// be available on this installation.
//
// The phrase "isn't a recognized model" is load-bearing: the Claude Code
// statusline greps for it to classify this ack as a no-op that leaves the
// prior pin intact (see install/cc-statusline.sh). Keep it verbatim.
func renderForceModelRejection(input string, entries []forceModelListEntry, format translate.Format) string {
	suggestions := suggestModelIDs(input, entries)
	if format == translate.FormatOpenAI {
		msg := fmt.Sprintf("Weave Router: force-model: %q isn't a recognized model; keeping automatic routing.", input)
		if len(suggestions) > 0 {
			return msg + " Did you mean: " + strings.Join(suggestions, ", ") + "? Run /force-model with no argument to list every model you can pin."
		}
		return msg + " Run /force-model with no argument to list every model you can pin."
	}
	msg := fmt.Sprintf("✦ **Weave Router** → force-model: %q isn't a recognized model · keeping automatic routing.", input)
	if len(suggestions) > 0 {
		msg += " Did you mean `" + strings.Join(suggestions, "`, `") + "`?"
	}
	return msg + " Run `/force-model` with no argument to list every model you can pin.\n\n"
}

// suggestModelIDs ranks pinnable ids by how well they match the user's input,
// comparing on the separator-folded form so "qwen 3.8" reaches
// "qwen/qwen3.8-max". A candidate qualifies on substring containment in either
// direction, or on a substantial shared prefix — the common typo ("qwen3.9")
// is a near-miss that contains nothing, so containment alone finds nothing to
// suggest exactly when the user most needs a hint.
func suggestModelIDs(input string, entries []forceModelListEntry) []string {
	key := foldModelSeparators(input)
	if key == "" {
		return nil
	}
	type scored struct {
		id      string
		shared  int
		lenDiff int
	}
	var matches []scored
	for _, e := range entries {
		folded := foldModelSeparators(e.ID)
		// The id's own trailing segment is compared too, so a vendor-prefixed
		// id ("qwen/qwen3.8-max") is reachable from a bare name.
		_, tail, hasSlash := strings.Cut(e.ID, "/")
		compared := folded
		shared := sharedPrefixLen(folded, key)
		if hasSlash {
			if foldedTail := foldModelSeparators(tail); sharedPrefixLen(foldedTail, key) > shared {
				shared = sharedPrefixLen(foldedTail, key)
				compared = foldedTail
			}
		}
		contained := strings.Contains(folded, key) || strings.Contains(key, folded)
		if !contained && shared < minSuggestionPrefix {
			continue
		}
		matches = append(matches, scored{
			id:      e.ID,
			shared:  shared,
			lenDiff: abs(len(compared) - len(key)),
		})
	}
	// Longest shared prefix first, then the closest length: among ids sharing
	// "qwen3", "qwen3.8-max" is a far likelier reading of "qwen3.9" than
	// "qwen3-235b-a22b-2507", and alphabetical order alone buries it.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].shared != matches[j].shared {
			return matches[i].shared > matches[j].shared
		}
		if matches[i].lenDiff != matches[j].lenDiff {
			return matches[i].lenDiff < matches[j].lenDiff
		}
		return matches[i].id < matches[j].id
	})
	out := make([]string, 0, maxSuggestions)
	for _, m := range matches {
		if len(out) == maxSuggestions {
			break
		}
		out = append(out, m.id)
	}
	return out
}

// minSuggestionPrefix is how many leading characters a near-miss must share
// before it's offered. Four keeps "qwen3.9" reaching the qwen family without
// letting every "g"-initial id answer a "gpt" typo.
const minSuggestionPrefix = 4

// sharedPrefixLen returns how many leading bytes a and b have in common.
func sharedPrefixLen(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// abs returns the absolute value of n.
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
