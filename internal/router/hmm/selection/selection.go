// Package selection ports the HMM sidecar's deterministic within-cluster arm
// selection (sidecars/hmm/hmm_sidecar/policy.py select_roster_group /
// select_roster_arm) to Go, for shadow comparison only. Nothing here serves.
package selection

import (
	"workweave/router/internal/router/hmm/rosterdata"
)

// Pick is the deterministic selection for one decision.
type Pick struct {
	// Group is the cluster label whose roster produced the arm.
	Group string
	// Arm is the first arm of the group's order present in the candidate set.
	Arm string
	// FallbackDepth is how many ranked groups had no eligible arm before Group.
	FallbackDepth int
	// HarnessOrder reports whether a harness-specific order was used.
	HarnessOrder bool
}

// ArmOrder returns the cluster's arm order for a harness: the harness-specific
// list when the roster declares a non-empty one, else the pooled cluster order.
// The public sidecar orders by cluster arms only; per-harness ordering is a
// roster-data extension the private sidecar consumes, mirrored here from the
// generated arms_by_harness field.
func ArmOrder(cluster rosterdata.Cluster, harness string) (order []string, harnessSpecific bool) {
	if arms := cluster.ArmsByHarness[harness]; len(arms) > 0 {
		return arms, true
	}
	return cluster.Arms, false
}

// Select walks rankedGroups in order and returns the first group with a roster
// arm present in candidates, picking that group's rank-1 eligible arm. This
// mirrors the sidecar's select_roster_group: the ranked order there is class
// labels sorted by (-probability, class index), which is exactly the order the
// sidecar reports in ranked_fallback, so callers pass that order through.
//
// Ties are impossible by construction: the order is a list, and the first
// candidate-set member wins deterministically.
//
// The private sidecar may additionally clamp the selected group by mode /
// turn type and filter membership per harness (membership_by_harness); the
// public sidecar (sidecars/hmm/) does neither, so neither is applied here.
func Select(roster *rosterdata.Roster, rankedGroups []string, harness string, candidates map[string]struct{}) (Pick, bool) {
	depth := 0
	for _, group := range rankedGroups {
		cluster, ok := roster.Clusters[group]
		if !ok {
			// A ranked label absent from the roster contributes no arms; the
			// sidecar walks it the same way (clusters.get(label) or {}).
			depth++
			continue
		}
		order, harnessSpecific := ArmOrder(cluster, harness)
		for _, arm := range order {
			if _, eligible := candidates[arm]; eligible {
				return Pick{Group: group, Arm: arm, FallbackDepth: depth, HarnessOrder: harnessSpecific}, true
			}
		}
		depth++
	}
	return Pick{}, false
}
