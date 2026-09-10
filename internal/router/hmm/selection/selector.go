package selection

import (
	"context"
	"fmt"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

// ErrNoEligibleArm is returned when no ranked group holds an eligible arm.
var ErrNoEligibleArm = policy.ErrNoEligibleArm

// Selector returns the deterministic arm selector backed by roster.
func Selector(roster *rosterdata.Roster) policy.ArmSelector {
	return SetSelector(rosterdata.NewSet(roster))
}

// SetSelector selects from the boot-loaded roster set: the default roster, or
// the roster a policy pin names via SelectionInput.RosterSHA256. An unknown
// digest fails closed with router.ErrPolicyPinUnavailable.
func SetSelector(rosters *rosterdata.Set) policy.ArmSelector {
	return func(ctx context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		log := observability.FromContext(ctx)
		roster, ok := rosters.Lookup(input.RosterSHA256)
		if !ok {
			return policy.SelectionPick{}, fmt.Errorf("roster %q not loaded: %w", input.RosterSHA256, router.ErrPolicyPinUnavailable)
		}
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
		pick, scoresByGroup, ok := SelectGroupsWithPreference(roster, groups, input.Harness, candidates, input.QualityBias)
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
		logFields := []any{"strategy", input.Strategy, "group", pick.Group, "arm", pick.Arm}
		if input.QualityBias != nil {
			logFields = append(logFields,
				"quality_bias", *input.QualityBias,
				"effective_alpha", EffectiveAlpha(roster, pick.Group, *input.QualityBias),
			)
		}
		log.Debug("HMM preference-aware arm selected", logFields...)
		return policy.SelectionPick{Group: pick.Group, Arm: pick.Arm, ArmScoresByGroup: scoresByGroup, RosterSHA256: roster.SHA256}, nil
	}
}
