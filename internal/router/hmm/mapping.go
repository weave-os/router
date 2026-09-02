package hmm

import (
	"strings"

	"workweave/router/internal/providers"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/translate"
)

var rosterAliases = map[string]string{
	"claude-sonnet-4-6":    "anthropic/claude-sonnet-4.6",
	"claude-haiku-4-5":     "anthropic/claude-haiku-4.5",
	"claude-sonnet-5":      "anthropic/claude-sonnet-5",
	"claude-opus-4-8":      "anthropic/claude-opus-4.8",
	"claude-fable-5":       "anthropic/claude-fable-5",
	"claude-fable-5-1":     "anthropic/claude-fable-5.1",
	"moonshotai/kimi-k2.7": "moonshotai/kimi-k2.7-code",
	// Bare first-party xAI IDs have no provider prefix to inherit, and the
	// switch below deliberately stays empty for them (a bare ID whose primary
	// provider is a hosting platform would be ambiguous). An explicit alias is
	// what makes an xAI-native model roster-addressable.
	"grok-4.5": "x-ai/grok-4.5",
	"grok-4.6": "x-ai/grok-4.6",
}

func rosterIDFor(m catalog.Model) string {
	if alias, ok := rosterAliases[m.ID]; ok {
		return alias
	}
	if strings.Contains(m.ID, "/") {
		return m.ID
	}
	switch m.PrimaryProvider() {
	case providers.ProviderAnthropic:
		return "anthropic/" + m.ID
	case providers.ProviderOpenAI:
		return "openai/" + m.ID
	case providers.ProviderGoogle:
		return "google/" + m.ID
	}
	return ""
}

// SplitEffort splits a roster arm ID of the form "model:effort" into its base model and canonical effort level; bare arms return (armID, "").
func SplitEffort(armID string) (baseID string, effort string) {
	if idx := strings.LastIndex(armID, ":"); idx > 0 {
		suffix := armID[idx+1:]
		if translate.CanonicalizeEffort(suffix) != suffix {
			return armID, ""
		}
		if translate.IsValidEffort(suffix) {
			return armID[:idx], suffix
		}
	}
	return armID, ""
}

// EffortArm composes a roster arm ID from a base model ID and effort level.
func EffortArm(baseID, effort string) string {
	if effort == "" {
		return baseID
	}
	return baseID + ":" + effort
}

// CatalogIDForRoster maps a roster arm ID back to its catalog model ID via the
// same forward mapping the resolver uses. Returns the arm ID unchanged when no
// catalog model maps to it.
func CatalogIDForRoster(rosterID string) string {
	baseID, _ := SplitEffort(rosterID)
	for _, m := range catalog.Models {
		if rosterIDFor(m) == baseID {
			return m.ID
		}
	}
	return rosterID
}
