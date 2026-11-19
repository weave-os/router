package catalog

import (
	"cmp"
	"slices"

	"weave-os/router/internal/providers"
)

func ClassifierRescueOrder(ids []string) []string {
	var models []Model
	for _, id := range ids {
		if m, ok := ByID(id); ok && !m.ForceModelOnly && m.Tier != TierUnknown && slices.ContainsFunc(m.Providers, func(b ProviderBinding) bool { return b.Provider == providers.ProviderAnthropic }) {
			models = append(models, m)
		}
	}
	slices.SortStableFunc(models, func(a, b Model) int {
		return cmp.Or(cmp.Compare(b.Tier, a.Tier), cmp.Compare(b.AdvisorRank, a.AdvisorRank), cmp.Compare(a.ID, b.ID))
	})
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}
