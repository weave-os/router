package armid

import (
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

var rosterAliases = map[string]string{
	"claude-sonnet-4-6":    "anthropic/claude-sonnet-4.6",
	"claude-haiku-4-5":     "anthropic/claude-haiku-4.5",
	"claude-haiku-5-5":     "anthropic/claude-haiku-5.5",
	"claude-sonnet-5":      "anthropic/claude-sonnet-5",
	"claude-opus-4-8":      "anthropic/claude-opus-4.8",
	"claude-fable-5":       "anthropic/claude-fable-5",
	"claude-fable-5-1":     "anthropic/claude-fable-5.1",
	"claude-fable-5-2":     "anthropic/claude-fable-5.2",
	"claude-opus-5-5":      "anthropic/claude-opus-5.5",
	"claude-sonnet-5-5":    "anthropic/claude-sonnet-5.5",
	"moonshotai/kimi-k2.7": "moonshotai/kimi-k2.7-code",
	"grok-4.5":             "x-ai/grok-4.5",
	"grok-4.6":             "x-ai/grok-4.6",
	"grok-4.7":             "x-ai/grok-4.7",
	"muse-spark-1.3":       "meta/muse-spark-1.3",
	"muse-ember-1.0":       "meta/muse-ember-1.0",
	"muse-wonder-1.0":      "meta/muse-wonder-1.0",
}

func ForModel(m catalog.Model) string {
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

type ArmID struct {
	Base   string
	Effort string
}

func Parse(rosterArm string) ArmID {
	idx := strings.LastIndex(rosterArm, ":")
	if idx <= 0 {
		return ArmID{Base: rosterArm}
	}
	suffix := rosterArm[idx+1:]
	if router.CanonicalizeEffort(suffix) != suffix || !router.IsValidEffort(suffix) {
		return ArmID{Base: rosterArm}
	}
	return ArmID{Base: rosterArm[:idx], Effort: suffix}
}

func CatalogIDForRoster(rosterID string) string {
	baseID := Parse(rosterID).Base
	for _, m := range catalog.Models {
		if ForModel(m) == baseID {
			return m.ID
		}
	}
	return rosterID
}

func (a ArmID) String() string {
	if a.Effort == "" {
		return a.Base
	}
	return a.Base + ":" + a.Effort
}
