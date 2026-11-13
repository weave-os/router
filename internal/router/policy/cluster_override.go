package policy

import (
	"errors"
	"fmt"
)

// ErrForcedClusterUnservable is returned when a caller forces a classifier
// group that this request cannot be served from — the label is not in the live
// roster, or it is but has no eligible arm left after exclusions, capability
// filtering, and any per-key allowlist.
var ErrForcedClusterUnservable = errors.New("forced cluster has no eligible model")

// ForcedClusterUnservableError carries the caller-facing reason a forced
// cluster was refused, so the dispatch classifier can name the cluster.
type ForcedClusterUnservableError struct {
	Cluster string
	Reason  string
}

// Error implements error.
func (e *ForcedClusterUnservableError) Error() string { return e.Reason }

// Unwrap ties the typed error to ErrForcedClusterUnservable for errors.Is.
func (e *ForcedClusterUnservableError) Unwrap() error { return ErrForcedClusterUnservable }

// ClusterOverrideResult is the outcome of applying per-key cluster allowlists
// to a sidecar decision.
type ClusterOverrideResult struct {
	// RosterID is the selected roster arm after overrides.
	RosterID string
	// ArmID is the resolver arm ID for the selected roster arm. On arm-enumerating
	// resolvers a roster ID can be ambiguous (shared across providers) and absent
	// from ByRosterID; the arm ID is always unambiguous.
	ArmID string
	// Group is the classifier group the selection came from.
	Group string
	// Applied is true when an override matched at least one ranked group and
	// produced a definite selection. When false the caller fails open.
	Applied bool
	// Changed is true when the override selected a different arm than the
	// sidecar's own pick (for reason annotation and telemetry).
	Changed bool
	// Constrained is true when a configured per-key list (or a forced label)
	// applies; false when the group's own eligible arms pass through unfiltered.
	Constrained bool
}

// ApplyClusterArmOverrides re-selects the served arm under per-key cluster
// allowlists. Walks ranked fallback in order; for each group intersects the
// override's catalog IDs (mapped to roster IDs) with eligible candidates so
// global exclusions still win. Fail-open when no override matches any group.
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

	// Overrides were configured but emptied every ranked group's arms. Report
	// not-applied so the caller keeps the sidecar's selection (fail-open); the
	// alternative — no eligible arm anywhere — would hard-fail the turn.
	return ClusterOverrideResult{}
}

// ApplyClusterArmOverridesRequireMatch is ApplyClusterArmOverrides's fail-closed
// sibling for a caller-forced cluster. A per-key allowlist is control-plane
// config validated against the roster at write time, so falling open to the
// sidecar's own pick is right for it; a forced label is unvalidated caller input
// on this turn, so an unmatched or emptied group is an error rather than a
// silent fall-through to a cluster the caller didn't ask for.
func ApplyClusterArmOverridesRequireMatch(
	overrides map[string][]string,
	rankedFallback []PreviewGroup,
	resolved ResolvedCandidates,
	sidecarRosterID string,
	requiredLabel string,
) (ClusterOverrideResult, error) {
	if len(rankedFallback) == 0 {
		// Opposite polarity to the fail-open path above: with no ranked fallback
		// there is no roster to prove the constraint against, and serving the
		// sidecar's unconstrained pick would silently ignore the force.
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

// candidateIndex is the per-request lookup set both override paths need.
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

// effectiveArms returns the ordered eligible roster arms for one ranked group
// under an optional override.
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
	// An override may add models the artifact never placed in this cluster, so
	// eligibility uses the full request-resolved candidate set (honors global
	// exclusions/provider filters), not just this group's artifact arms.
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
