package hmm

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm/armid"
)

// Deprecated: silent-drop let inert roster arms skew production routing; replaced
// by fail-loud validation (rosterdata.Load / ValidateRosterIDs). Still used by the
// admin roster view; removed once that reads the declarative roster.
// See docs/HMM_GO_SELECTION.md.
func DeployedModelsForRosterIDs(rosterIDs []string) []router.DeployedEntry {
	inverse := make(map[string]catalog.Model, len(catalog.Models))
	for _, m := range catalog.Models {
		rosterID := armid.ForModel(m)
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
		m, ok := inverse[armid.Parse(rosterID).Base]
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
