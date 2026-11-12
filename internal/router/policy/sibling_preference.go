package policy

import (
	"maps"
	"slices"
	"sort"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
)

// applySiblingPreferences only reorders already eligible arms within a group.
// It never changes the classifier's group or admits an excluded roster member.
func applySiblingPreferences(req router.Request, resolved ResolvedCandidates, groups []PreviewGroup) []PreviewGroup {
	if len(req.SiblingPreferences) == 0 {
		return groups
	}
	groups = slices.Clone(groups)
	index := indexCandidates(resolved)
	for i, group := range groups {
		preference, remembered := req.SiblingPreferences[escalation.Group(group.Group)]
		if !remembered {
			continue
		}
		override, hasOverride := req.ClusterArmOverrides[group.Group]
		arms := slices.Clone(effectiveArms(group, override, hasOverride, index.catalogToRoster, index.eligibleRosterIDs))
		// Arm-enumerating strategies list arm IDs here; normalize each entry to
		// its roster ID so eligibility and ordering apply uniformly.
		for i, arm := range arms {
			base, effort := splitEffort(arm)
			if _, eligible := index.eligibleRosterIDs[base]; eligible {
				continue
			}
			rosterID, mapped := index.armToRoster[base]
			if !mapped {
				continue
			}
			if effort != "" {
				rosterID += ":" + effort
			}
			arms[i] = rosterID
		}
		seen := make(map[string]struct{}, len(arms))
		arms = slices.DeleteFunc(arms, func(arm string) bool {
			base, _ := splitEffort(arm)
			if _, eligible := index.eligibleRosterIDs[base]; !eligible {
				return true
			}
			if _, dup := seen[base]; dup {
				return true
			}
			seen[base] = struct{}{}
			return false
		})
		rank := func(arm string) int {
			binding, ok := resolved.BindingForSelection(siblingArmID(index, arm), arm)
			if !ok {
				return 1
			}
			if binding.CatalogID == preference.Model {
				return 0
			}
			if slices.Contains(preference.DisplacedModels, binding.CatalogID) {
				return 2
			}
			return 1
		}
		sort.SliceStable(arms, func(i, j int) bool { return rank(arms[i]) < rank(arms[j]) })
		groups[i].EligibleArms = arms
	}
	return groups
}

func siblingArmID(index candidateIndex, rosterArm string) string {
	base, effort := splitEffort(rosterArm)
	armID := index.rosterToArm[base]
	if armID != "" && effort != "" {
		return armID + ":" + effort
	}
	return armID
}

func preferredSiblingArm(req router.Request, groups []PreviewGroup, selectedGroup, selectedArm string) string {
	if _, remembered := req.SiblingPreferences[escalation.Group(selectedGroup)]; remembered {
		for _, group := range groups {
			if group.Group == selectedGroup && len(group.EligibleArms) > 0 {
				return group.EligibleArms[0]
			}
		}
	}
	return selectedArm
}

// The effective arms already include configured cluster lists. Applying a
// list again during rescue would undo its remembered sibling ordering.
func siblingPreferenceOverrides(req router.Request) map[string][]string {
	if len(req.SiblingPreferences) == 0 {
		return req.ClusterArmOverrides
	}
	overrides := maps.Clone(req.ClusterArmOverrides)
	for group := range req.SiblingPreferences {
		delete(overrides, string(group))
	}
	return overrides
}

func siblingModelsForGroup(group string, groups []PreviewGroup, effectiveOverrides, configuredOverrides map[string][]string, resolved ResolvedCandidates) []string {
	for _, candidate := range groups {
		if candidate.Group == group {
			members := RescueModelOrder(effectiveOverrides, []PreviewGroup{candidate}, resolved)
			for _, model := range RescueCoolingPool(configuredOverrides, []PreviewGroup{candidate}, resolved) {
				if !slices.Contains(members, model) {
					members = append(members, model)
				}
			}
			return members
		}
	}
	return nil
}
