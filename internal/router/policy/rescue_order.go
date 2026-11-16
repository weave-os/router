package policy

import "weave-os/router/internal/router/escalation"

func EscalatingRescueGroups(selectedGroup, forcedGroup string, rankedFallback []PreviewGroup) []PreviewGroup {
	if forcedGroup != "" {
		for _, group := range rankedFallback {
			if group.Group == forcedGroup {
				return []PreviewGroup{group}
			}
		}
		return nil
	}
	start := escalation.Rank(escalation.Group(selectedGroup))
	if start < 0 {
		return rankedFallback
	}
	groups := make([]PreviewGroup, 0, len(rankedFallback))
	for rank := start; rank <= escalation.Rank(escalation.Maximum); rank++ {
		for _, group := range rankedFallback {
			if escalation.Rank(escalation.Group(group.Group)) == rank {
				groups = append(groups, group)
			}
		}
	}
	return groups
}

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

func RescueCoolingPool(overrides map[string][]string, groups []PreviewGroup, resolved ResolvedCandidates) []string {
	softExcluded := make(map[string]string)
	softExcludedCatalog := make(map[string]struct{})
	for _, diagnostic := range resolved.Diagnostics {
		if diagnostic.Reason == ExclusionAutomaticDisabled {
			softExcluded[diagnostic.RosterID] = diagnostic.CatalogID
			softExcludedCatalog[diagnostic.CatalogID] = struct{}{}
		}
	}
	var pool []string
	seen := make(map[string]struct{})
	for _, group := range groups {
		if override, configured := overrides[group.Group]; configured {
			for _, catalogID := range override {
				if _, eligible := softExcludedCatalog[catalogID]; !eligible {
					continue
				}
				if _, duplicate := seen[catalogID]; !duplicate {
					pool = append(pool, catalogID)
					seen[catalogID] = struct{}{}
				}
			}
			continue
		}
		for _, arm := range group.RosterArms {
			rosterID, _ := splitEffort(arm)
			model, eligible := softExcluded[rosterID]
			if !eligible {
				continue
			}
			if _, duplicate := seen[model]; duplicate {
				continue
			}
			pool = append(pool, model)
			seen[model] = struct{}{}
		}
	}
	return pool
}
