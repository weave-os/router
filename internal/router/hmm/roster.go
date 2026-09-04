package hmm

import (
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
)

// DeployedModelsForRosterIDs maps sidecar roster IDs to catalog {model, provider} entries.
// Unknown IDs are dropped; first occurrence wins on duplicates.
// Effort-suffixed arms (e.g. "anthropic/claude-opus-5:xhigh") are mapped to
// their base catalog model.
//
// Deprecated: silent-drop let inert roster arms skew production routing; replaced
// by fail-loud validation (rosterdata.Load / ValidateRosterIDs). Still used by the
// admin roster view; removed once that reads the declarative roster.
// See docs/HMM_GO_SELECTION.md.
func DeployedModelsForRosterIDs(rosterIDs []string) []cluster.DeployedEntry {
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

	out := make([]cluster.DeployedEntry, 0, len(rosterIDs))
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
		out = append(out, cluster.DeployedEntry{Model: m.ID, Provider: m.PrimaryProvider()})
	}
	return out
}
