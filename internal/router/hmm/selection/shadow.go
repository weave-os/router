package selection

import (
	"context"

	"workweave/router/internal/observability"
	"workweave/router/internal/router/hmm/rosterdata"
	"workweave/router/internal/router/policy"
)

// Shadow returns a log-only observer that recomputes the deterministic pick
// from roster and logs agreement with the sidecar's served pick.
func Shadow(roster *rosterdata.Roster) policy.SelectionShadow {
	return func(ctx context.Context, observation policy.SelectionObservation) {
		log := observability.FromContext(ctx)
		if len(observation.RankedFallback) == 0 {
			log.Debug("HMM selection shadow skipped: sidecar reported no ranked fallback",
				"strategy", observation.Strategy,
				"execution_mode", observation.ExecutionMode,
				"route_id", observation.RouteID,
			)
			return
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
			log.Warn("HMM selection shadow found no eligible arm in any ranked group",
				"strategy", observation.Strategy,
				"execution_mode", observation.ExecutionMode,
				"route_id", observation.RouteID,
				"harness", observation.Harness,
				"ranked_groups", rankedGroups,
				"candidate_roster_ids", observation.CandidateRosterIDs,
				"sidecar_group", observation.SidecarGroup,
				"sidecar_arm", observation.SidecarPick,
			)
			return
		}
		log.Info("HMM selection shadow compared",
			"strategy", observation.Strategy,
			"execution_mode", observation.ExecutionMode,
			"route_id", observation.RouteID,
			"agree", pick.Arm == observation.SidecarPick && pick.Group == observation.SidecarGroup,
			"shadow_group", pick.Group,
			"shadow_arm", pick.Arm,
			"sidecar_group", observation.SidecarGroup,
			"sidecar_arm", observation.SidecarPick,
			"fallback_depth", pick.FallbackDepth,
			"harness", observation.Harness,
			"harness_order", pick.HarnessOrder,
			"ranked_groups", rankedGroups,
			"candidate_roster_ids", observation.CandidateRosterIDs,
		)
	}
}
