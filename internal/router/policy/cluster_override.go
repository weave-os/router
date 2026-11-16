package policy

import (
	"errors"
	"fmt"
)

var ErrForcedClusterUnservable = errors.New("forced cluster has no eligible model")

type ForcedClusterUnservableError struct {
	Cluster string
	Reason  string
}

func (e *ForcedClusterUnservableError) Error() string { return e.Reason }

func (e *ForcedClusterUnservableError) Unwrap() error { return ErrForcedClusterUnservable }

type ClusterOverrideResult struct {
	RosterID    string
	ArmID       string
	Group       string
	Applied     bool
	Changed     bool
	Constrained bool
}

func ApplyClusterArmOverrides(
	overrides map[string][]string,
	rankedFallback []PreviewGroup,
	resolved ResolvedCandidates,
	sidecarRosterID string,
) ClusterOverrideResult {
	if len(overrides) == 0 || len(rankedFallback) == 0 {
		return ClusterOverrideResult{}
	}

	index := indexCandidates(resolved)

	for _, group := range rankedFallback {
		override, hasOverride := overrides[group.Group]
		effective := effectiveArms(group, override, hasOverride, index.catalogToRoster, index.eligibleRosterIDs)
		if len(effective) == 0 {
			continue
		}
		selected := effective[0]
		return ClusterOverrideResult{
			RosterID:    selected,
			ArmID:       index.rosterToArm[selected],
			Group:       group.Group,
			Applied:     true,
			Changed:     selected != sidecarRosterID,
			Constrained: hasOverride,
		}
	}

	return ClusterOverrideResult{}
}

func ApplyClusterArmOverridesRequireMatch(
	overrides map[string][]string,
	rankedFallback []PreviewGroup,
	resolved ResolvedCandidates,
	sidecarRosterID string,
	requiredLabel string,
) (ClusterOverrideResult, error) {
	if len(rankedFallback) == 0 {
		return ClusterOverrideResult{}, &ForcedClusterUnservableError{
			Cluster: requiredLabel,
			Reason:  fmt.Sprintf("cannot force cluster %q: the policy sidecar does not report its routing clusters", requiredLabel),
		}
	}

	var matched PreviewGroup
	found := false
	for _, group := range rankedFallback {
		if group.Group == requiredLabel {
			matched = group
			found = true
			break
		}
	}
	if !found {
		return ClusterOverrideResult{}, &ForcedClusterUnservableError{
			Cluster: requiredLabel,
			Reason:  fmt.Sprintf("%q is not a routing cluster on this installation", requiredLabel),
		}
	}

	index := indexCandidates(resolved)
	override, hasOverride := overrides[requiredLabel]
	effective := effectiveArms(matched, override, hasOverride, index.catalogToRoster, index.eligibleRosterIDs)
	if len(effective) == 0 {
		return ClusterOverrideResult{}, &ForcedClusterUnservableError{
			Cluster: requiredLabel,
			Reason:  fmt.Sprintf("no model in cluster %q can serve this request", requiredLabel),
		}
	}

	selected := effective[0]
	return ClusterOverrideResult{
		RosterID:    selected,
		ArmID:       index.rosterToArm[selected],
		Group:       requiredLabel,
		Applied:     true,
		Changed:     selected != sidecarRosterID,
		Constrained: true,
	}, nil
}

type candidateIndex struct {
	catalogToRoster   map[string]string
	rosterToArm       map[string]string
	eligibleRosterIDs map[string]struct{}
}

func indexCandidates(resolved ResolvedCandidates) candidateIndex {
	index := candidateIndex{
		catalogToRoster:   make(map[string]string, len(resolved.Candidates)),
		rosterToArm:       make(map[string]string, len(resolved.Candidates)),
		eligibleRosterIDs: make(map[string]struct{}, len(resolved.Candidates)),
	}
	for _, candidate := range resolved.Candidates {
		if _, exists := index.catalogToRoster[candidate.CatalogID]; !exists {
			index.catalogToRoster[candidate.CatalogID] = candidate.RosterID
		}
		if _, exists := index.rosterToArm[candidate.RosterID]; !exists {
			index.rosterToArm[candidate.RosterID] = candidate.ArmID
		}
		index.eligibleRosterIDs[candidate.RosterID] = struct{}{}
	}
	return index
}

func effectiveArms(
	group PreviewGroup,
	override []string,
	hasOverride bool,
	catalogToRoster map[string]string,
	eligibleRosterIDs map[string]struct{},
) []string {
	if !hasOverride {
		return group.EligibleArms
	}
	out := make([]string, 0, len(override))
	seen := make(map[string]struct{}, len(override))
	for _, catalogID := range override {
		rosterID, mapped := catalogToRoster[catalogID]
		if !mapped {
			continue
		}
		if _, eligible := eligibleRosterIDs[rosterID]; !eligible {
			continue
		}
		if _, dup := seen[rosterID]; dup {
			continue
		}
		seen[rosterID] = struct{}{}
		out = append(out, rosterID)
	}
	return out
}
