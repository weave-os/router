package selection

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

// ErrNoEligibleArm is returned when no ranked group holds an eligible arm.
var ErrNoEligibleArm = policy.ErrNoEligibleArm

// ErrClassifierTaxonomyMismatch means the classifier and Go policy disagree on
// the ordered class taxonomy; retrying another fallback group cannot fix it.
var ErrClassifierTaxonomyMismatch = errors.New("classifier taxonomy does not match serving roster")

// Selector returns the deterministic arm selector backed by roster.
func Selector(roster *rosterdata.Roster) policy.ArmSelector {
	return func(ctx context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		log := observability.FromContext(ctx)
		if input.RosterSHA256 != "" && input.RosterSHA256 != roster.SHA256 {
			return policy.SelectionPick{}, fmt.Errorf("roster %q is not the loaded serving roster: %w", input.RosterSHA256, router.ErrPolicyPinUnavailable)
		}
		if len(roster.ClassOrder) > 0 && !slices.Equal(input.ClassOrder, roster.ClassOrder) {
			return policy.SelectionPick{}, fmt.Errorf("classifier class order does not match the promoted roster: %w", ErrClassifierTaxonomyMismatch)
		}
		rankedGroups, err := classifierGroups(input)
		if err != nil {
			return policy.SelectionPick{}, err
		}
		if input.ForcedGroup != "" {
			if _, exists := roster.Clusters[input.ForcedGroup]; !exists {
				return policy.SelectionPick{}, fmt.Errorf("forced group %q is absent from policy: %w", input.ForcedGroup, ErrNoEligibleArm)
			}
			rankedGroups = []string{input.ForcedGroup}
		}
		groups := make([]Group, 0, len(rankedGroups))
		for _, group := range rankedGroups {
			groups = append(groups, Group{Label: group})
		}
		candidates := make(map[string]struct{}, len(input.CandidateRosterIDs))
		for _, rosterID := range input.CandidateRosterIDs {
			candidates[rosterID] = struct{}{}
		}
		pick, scoresByGroup, scoreComponentsByGroup, ordersByGroup, ok := SelectGroupsWithPreferences(
			roster,
			groups,
			input.Harness,
			candidates,
			input.QualityBias,
			input.PreferredModels,
			input.SubscriptionStatePreferredModels,
			input.SubsidizedModelCostFactor,
		)
		if !ok {
			log.Warn("HMM selection found no eligible arm in any ranked group",
				"strategy", input.Strategy,
				"execution_mode", input.ExecutionMode,
				"route_id", input.RouteID,
				"harness", input.Harness,
				"ranked_groups", rankedGroups,
				"candidate_roster_ids", input.CandidateRosterIDs,
				"predicted_label", input.PredictedLabel,
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
		fallback := make([]policy.PreviewGroup, 0, len(rankedGroups))
		for _, label := range rankedGroups {
			cluster, exists := roster.Clusters[label]
			if !exists {
				fallback = append(fallback, policy.PreviewGroup{Group: label, Probability: input.ClassProbabilities[label]})
				continue
			}
			order := ordersByGroup[label]
			eligible := eligibleOrder(order, candidates)
			fallback = append(fallback, policy.PreviewGroup{Group: label, Probability: input.ClassProbabilities[label], RosterArms: append([]string(nil), cluster.Arms...), EligibleArms: eligible})
		}
		return policy.SelectionPick{
			Group: pick.Group, Arm: pick.Arm, ArmScoresByGroup: scoresByGroup, RankedFallback: fallback, RosterSHA256: roster.SHA256,
			Trace: policy.SelectionTrace{
				ClassifierRanking:                append([]string(nil), rankedGroups...),
				Harness:                          input.Harness,
				ForcedGroup:                      input.ForcedGroup,
				CandidateRosterIDs:               append([]string(nil), input.CandidateRosterIDs...),
				QualityBias:                      input.QualityBias,
				PreferredModels:                  append([]string(nil), input.PreferredModels...),
				SubscriptionStatePreferredModels: append([]string(nil), input.SubscriptionStatePreferredModels...),
				SubsidizedModelCostFactor:        cloneModelFactors(input.SubsidizedModelCostFactor),
				EffectiveOrders:                  ordersByGroup,
				ScoresByGroup:                    scoresByGroup,
				ScoreComponentsByGroup:           scoreComponentsByGroup,
				SelectedGroup:                    pick.Group,
				SelectedArm:                      pick.Arm,
				FallbackDepth:                    pick.FallbackDepth,
			},
		}, nil
	}
}

func cloneModelFactors(factors map[string]float64) map[string]float64 {
	if len(factors) == 0 {
		return nil
	}
	cloned := make(map[string]float64, len(factors))
	for model, factor := range factors {
		cloned[model] = factor
	}
	return cloned
}

func classifierGroups(input policy.SelectionInput) ([]string, error) {
	if len(input.ClassOrder) == 0 || len(input.ClassProbabilities) != len(input.ClassOrder) {
		return nil, fmt.Errorf("classifier returned an incomplete class contract: %w", ErrNoEligibleArm)
	}
	rank := make(map[string]int, len(input.ClassOrder))
	groups := append([]string(nil), input.ClassOrder...)
	total := 0.0
	for index, label := range input.ClassOrder {
		probability, exists := input.ClassProbabilities[label]
		if label == "" || !exists || math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 || probability > 1 {
			return nil, fmt.Errorf("classifier returned invalid probability for %q: %w", label, ErrNoEligibleArm)
		}
		if _, duplicate := rank[label]; duplicate {
			return nil, fmt.Errorf("classifier returned duplicate class %q: %w", label, ErrNoEligibleArm)
		}
		rank[label] = index
		total += probability
	}
	if math.Abs(total-1) > 1e-6 {
		return nil, fmt.Errorf("classifier probabilities sum to %.9f: %w", total, ErrNoEligibleArm)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		left, right := input.ClassProbabilities[groups[i]], input.ClassProbabilities[groups[j]]
		if left != right {
			return left > right
		}
		return rank[groups[i]] < rank[groups[j]]
	})
	return groups, nil
}

func eligibleOrder(order []string, candidates map[string]struct{}) []string {
	eligible := make([]string, 0, len(order))
	for _, arm := range order {
		baseID, _ := hmm.SplitEffort(arm)
		if _, exists := candidates[baseID]; exists {
			eligible = append(eligible, arm)
		}
	}
	return eligible
}
