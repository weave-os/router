package selection

import (
	"context"
	"errors"
	"fmt"

	"workweave/router/internal/observability"
	"workweave/router/internal/router/hmm/rosterdata"
	"workweave/router/internal/router/policy"
)

// ErrNoEligibleArm is returned when no ranked group holds an eligible arm.
var ErrNoEligibleArm = errors.New("no eligible arm in any ranked group")

// Selector returns the deterministic arm selector backed by roster.
func Selector(roster *rosterdata.Roster) policy.ArmSelector {
	return func(ctx context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		log := observability.FromContext(ctx)
		if len(input.RankedFallback) == 0 {
			return policy.SelectionPick{}, fmt.Errorf("sidecar reported no ranked fallback: %w", ErrNoEligibleArm)
		}
		groups := make([]Group, 0, len(input.RankedFallback))
		rankedGroups := make([]string, 0, len(input.RankedFallback))
		for _, group := range input.RankedFallback {
			groups = append(groups, Group{Label: group.Group, AllowedArms: group.EligibleArms})
			rankedGroups = append(rankedGroups, group.Group)
		}
		candidates := make(map[string]struct{}, len(input.CandidateRosterIDs))
		for _, rosterID := range input.CandidateRosterIDs {
			candidates[rosterID] = struct{}{}
		}
		pick, ok := SelectGroups(roster, groups, input.Harness, candidates)
		if !ok {
			log.Warn("HMM selection found no eligible arm in any ranked group",
				"strategy", input.Strategy,
				"execution_mode", input.ExecutionMode,
				"route_id", input.RouteID,
				"harness", input.Harness,
				"ranked_groups", rankedGroups,
				"candidate_roster_ids", input.CandidateRosterIDs,
				"classifier_group", input.ClassifierGroup,
			)
			return policy.SelectionPick{}, ErrNoEligibleArm
		}
		return policy.SelectionPick{Group: pick.Group, Arm: pick.Arm}, nil
	}
}
