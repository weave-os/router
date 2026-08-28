package selection

import (
	"context"

	"workweave/router/internal/observability"
	"workweave/router/internal/router/hmm/rosterdata"
	"workweave/router/internal/router/policy"
)

// Override returns an authoritative re-selector that recomputes the
// deterministic pick from roster. It fails open (ok=false, sidecar pick
// serves) when the sidecar reports no ranked fallback or no ranked group
// holds an eligible arm.
func Override(roster *rosterdata.Roster) policy.SelectionOverride {
	return func(ctx context.Context, observation policy.SelectionObservation) (policy.SelectionPick, bool) {
		log := observability.FromContext(ctx)
		if len(observation.RankedFallback) == 0 {
			log.Warn("HMM Go selection skipped: sidecar reported no ranked fallback",
				"strategy", observation.Strategy,
				"execution_mode", observation.ExecutionMode,
				"route_id", observation.RouteID,
			)
			return policy.SelectionPick{}, false
		}
		rankedGroups := make([]string, 0, len(observation.RankedFallback))
		for _, group := range observation.RankedFallback {
			rankedGroups = append(rankedGroups, group.Group)
		}
		candidates := make(map[string]struct{}, len(observation.CandidateRosterIDs))
		for _, rosterID := range observation.CandidateRosterIDs {
			candidates[rosterID] = struct{}{}
		}
		pick, ok := Select(roster, rankedGroups, observation.Harness, candidates)
		if !ok {
			log.Warn("HMM Go selection found no eligible arm in any ranked group; serving sidecar pick",
				"strategy", observation.Strategy,
				"execution_mode", observation.ExecutionMode,
				"route_id", observation.RouteID,
				"harness", observation.Harness,
				"ranked_groups", rankedGroups,
				"candidate_roster_ids", observation.CandidateRosterIDs,
				"sidecar_group", observation.SidecarGroup,
				"sidecar_arm", observation.SidecarPick,
			)
			return policy.SelectionPick{}, false
		}
		return policy.SelectionPick{Group: pick.Group, Arm: pick.Arm}, true
	}
}
