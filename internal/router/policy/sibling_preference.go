package policy

import (
	"context"
	"maps"
	"slices"
	"sort"

	"weave-os/router/internal/observability"
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
		arms := effectiveArms(group, override, hasOverride, index.catalogToRoster, index.eligibleRosterIDs)
		arms = eligiblePreferenceArms(arms, index)
		rank := func(arm string) int { return siblingArmRank(preference, resolved, index, arm) }
		sort.SliceStable(arms, func(i, j int) bool { return rank(arms[i]) < rank(arms[j]) })
		groups[i].EligibleArms = arms
	}
	return groups
}

// eligiblePreferenceArms maps arm-enumerating entries to their roster IDs,
// drops anything ineligible, and keeps each normalized arm once.
func eligiblePreferenceArms(arms []string, index candidateIndex) []string {
	normalized := slices.Clone(arms)
	for i, arm := range normalized {
		normalized[i] = rosterArmForPreference(index, arm)
	}
	seen := make(map[string]struct{}, len(normalized))
	return slices.DeleteFunc(normalized, func(arm string) bool {
		base, _ := splitEffort(arm)
		if _, eligible := index.eligibleRosterIDs[base]; !eligible {
			return true
		}
		if _, dup := seen[arm]; dup {
			return true
		}
		seen[arm] = struct{}{}
		return false
	})
}

// Arm-enumerating strategies list arm IDs here; normalize each entry to its
// roster ID so eligibility and ordering apply uniformly.
func rosterArmForPreference(index candidateIndex, arm string) string {
	base, effort := splitEffort(arm)
	if _, eligible := index.eligibleRosterIDs[base]; eligible {
		return arm
	}
	rosterID, mapped := index.armToRoster[base]
	if !mapped {
		return arm
	}
	if effort != "" {
		return rosterID + ":" + effort
	}
	return rosterID
}

func siblingArmRank(preference router.SiblingPreference, resolved ResolvedCandidates, index candidateIndex, arm string) int {
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

// siblingOrderedSelection applies the remembered order to the pick's groups
// and returns the possibly reselected arm plus the derived override map.
func siblingOrderedSelection(req router.Request, resolved ResolvedCandidates, groups *[]PreviewGroup, group, arm string) (string, map[string][]string) {
	*groups = applySiblingPreferences(req, resolved, *groups)
	return preferredSiblingArm(req, *groups, group, arm), siblingPreferenceOverrides(req)
}

// siblingArmSelection is the routed-arm tuple a remembered preference may
// replace when it picks a different arm in the selected group.
type siblingArmSelection struct {
	RosterID   string
	ArmID      string
	Reason     router.SelectionOverrideReason
	Reselected bool
}

// reselectRememberedSibling swaps the override selection for the remembered
// group winner. The preference may reorder the selected group, never move the
// request to another group.
func reselectRememberedSibling(ctx context.Context, sel siblingArmSelection, req router.Request, resolved ResolvedCandidates, groups []PreviewGroup, group string) siblingArmSelection {
	if arm := preferredSiblingArm(req, groups, group, sel.RosterID); arm != sel.RosterID {
		sel.RosterID = arm
		sel.ArmID = siblingArmID(indexCandidates(resolved), arm)
		if sel.Reason == router.SelectionOverrideNone {
			sel.Reason = router.SelectionOverrideSibling
		}
		sel.Reselected = true
		observability.FromContext(ctx).Info("Session sibling preference applied", "group", group, "arm", arm)
	}
	return sel
}

// siblingOrderingForRoute produces the preference-ordered groups and the three
// override maps a route derives from them.
func siblingOrderingForRoute(req router.Request, resolved ResolvedCandidates, groups, rescueGroups []PreviewGroup, escalationConstrained bool) (ordered []PreviewGroup, effective, rescue, pool map[string][]string) {
	ordered = applySiblingPreferences(req, resolved, groups)
	effective, rescue, pool = siblingRoutingOverrides(req, rescueGroups, escalationConstrained)
	return ordered, effective, rescue, pool
}

// dropSiblingPreferencesOnPolicyPin makes an honoured policy pin authoritative:
// it chooses the arm itself, so remembered group orders do not apply.
func dropSiblingPreferencesOnPolicyPin(req *router.Request, pinned bool) {
	if pinned {
		req.SiblingPreferences = nil
	}
}

// siblingRoutingOverrides derives the three override maps a route needs once
// preferences reorder a group: the effective map, the rescue order's map, and
// the cooling pool's map. The pool keeps the configured allowlists: dropping a
// remembered group's list would readmit a soft-excluded model outside it.
func siblingRoutingOverrides(req router.Request, groups []PreviewGroup, escalationConstrained bool) (effective, rescue, pool map[string][]string) {
	effective = siblingPreferenceOverrides(req)
	rescue = effective
	pool = req.ClusterArmOverrides
	if escalationConstrained {
		groupNames := make([]string, 0, len(groups))
		for _, group := range groups {
			groupNames = append(groupNames, group.Group)
		}
		rescue = escalationArmOverrides(rescue, groupNames...)
		pool = escalationArmOverrides(pool, groupNames...)
	}
	return effective, rescue, pool
}

// recordRememberedOrders stamps each remembered group's effective arm order on
// the selection trace so replays see what the preference produced.
func recordRememberedOrders(trace *router.SelectionTrace, req router.Request, groups []PreviewGroup) {
	if len(req.SiblingPreferences) == 0 {
		return
	}
	trace.EffectiveOrders = maps.Clone(trace.EffectiveOrders)
	if trace.EffectiveOrders == nil {
		trace.EffectiveOrders = make(map[string][]string)
	}
	for _, group := range groups {
		if _, remembered := req.SiblingPreferences[escalation.Group(group.Group)]; remembered {
			trace.EffectiveOrders[group.Group] = slices.Clone(group.EligibleArms)
		}
	}
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
