package hmm

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func DeployedEntriesSkippingUnknownArms(rosterIDs []string) []router.DeployedEntry {
	inverse := make(map[string]catalog.Model, len(catalog.Models))
	for _, m := range catalog.Models {
		rosterID := rosterIDFor(m)
		if rosterID == "" {
			continue
		}
		if _, exists := inverse[rosterID]; !exists {
			inverse[rosterID] = m
		}
	}

	out := make([]router.DeployedEntry, 0, len(rosterIDs))
	seen := make(map[string]struct{}, len(rosterIDs))
	for _, rosterID := range rosterIDs {
		baseID, _ := SplitEffort(rosterID)
		m, ok := inverse[baseID]
		if !ok {
			continue
		}
		if _, dup := seen[m.ID]; dup {
			continue
		}
		seen[m.ID] = struct{}{}
		out = append(out, router.DeployedEntry{Model: m.ID, Provider: m.PrimaryProvider()})
	}
	return out
}
