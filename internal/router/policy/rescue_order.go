package policy

// RescueModelOrder lists the catalog models an in-turn rescue may fall back to
// once the served model's bindings fail, in ranked-fallback order: each group's
// effective arms (per-key allowlists applied, so a model the key excluded never
// re-enters through a rescue) mapped back to catalog IDs, first occurrence wins.
// Nil when the sidecar reports no ranked fallback.
func RescueModelOrder(
	overrides map[string][]string,
	rankedFallback []PreviewGroup,
	resolved ResolvedCandidates,
) []string {
	if len(rankedFallback) == 0 {
		return nil
	}
	index := indexCandidates(resolved)
	var order []string
	seen := make(map[string]struct{})
	for _, group := range rankedFallback {
		override, hasOverride := overrides[group.Group]
		for _, rosterID := range effectiveArms(group, override, hasOverride, index.catalogToRoster, index.eligibleRosterIDs) {
			binding, ok := resolved.BindingForSelection(index.rosterToArm[rosterID], rosterID)
			if !ok {
				continue
			}
			if _, dup := seen[binding.CatalogID]; dup {
				continue
			}
			seen[binding.CatalogID] = struct{}{}
			order = append(order, binding.CatalogID)
		}
	}
	return order
}
