// Package selection ports the HMM sidecar's deterministic within-cluster arm
// selection (sidecars/hmm/hmm_sidecar/policy.py select_roster_group /
// select_roster_arm) to Go. It serves via ROUTER_HMM_GO_SELECTION (default
// off) and is compared log-only via ROUTER_HMM_SELECTION_SHADOW; see
// docs/HMM_GO_SELECTION.md.
package selection

import (
	"strings"

	"workweave/router/internal/router/hmm"
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

// ArmOrder returns the harness-specific arm order when the roster declares a non-empty one, else the pooled order (private-sidecar arms_by_harness extension).
// Roster harness keys use underscores (claude_code) while router.Request.ClientApp is hyphenated (claude-code), so both spellings are tried.
func ArmOrder(cluster rosterdata.Cluster, harness string) (order []string, harnessSpecific bool) {
	if arms := cluster.ArmsByHarness[harness]; len(arms) > 0 {
		return arms, true
	}
	if arms := cluster.ArmsByHarness[strings.ReplaceAll(harness, "-", "_")]; len(arms) > 0 {
		return arms, true
	}
	return cluster.Arms, false
}

// Select walks rankedGroups and returns the first group whose roster arm is in
// candidates (rank-1 pick). rankedGroups must be pre-sorted by the sidecar's
// ranked_fallback order (desc probability). The private sidecar additionally
// clamps by mode/turn-type and filters via membership_by_harness; the public
// sidecar does neither, so neither is applied here.
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
			// Candidates carry base roster IDs, so effort-suffixed arms
			// (model:high) match on their base ID.
			baseID, _ := hmm.SplitEffort(arm)
			if _, eligible := candidates[baseID]; eligible {
				return Pick{Group: group, Arm: arm, FallbackDepth: depth, HarnessOrder: harnessSpecific}, true
			}
		}
		depth++
	}
	return Pick{}, false
}
