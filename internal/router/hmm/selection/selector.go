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
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selectionpolicy"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/taskdomain"
)

var ErrNoEligibleArm = policy.ErrNoEligibleArm

var ErrClassifierTaxonomyMismatch = errors.New("classifier taxonomy does not match serving roster")

var ErrInvalidClassifierOutput = errors.New("classifier returned an invalid class contract")

func Selector(admitted *selectionpolicy.Policy, evidence *DomainEvidence, evidenceSHA256 string) policy.ArmSelector {
	return selector(admitted.Roster(), admitted.CatalogID, evidence, evidenceSHA256)
}

func selector(roster *rosterdata.Roster, catalogID func(string) string, evidence *DomainEvidence, evidenceSHA256 string) policy.ArmSelector {
	return func(ctx context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		log := observability.FromContext(ctx)
		if input.RosterSHA256 != "" && input.RosterSHA256 != roster.SHA256 {
			return policy.SelectionPick{}, fmt.Errorf("roster %q is not the loaded serving roster: %w", input.RosterSHA256, router.ErrPolicyPinUnavailable)
		}
		if !input.Unscorable && len(roster.ClassOrder) > 0 && !slices.Equal(input.ClassOrder, roster.ClassOrder) {
			return policy.SelectionPick{}, fmt.Errorf("classifier class order does not match the promoted roster: %w", ErrClassifierTaxonomyMismatch)
		}
		rankedGroups, err := rankedGroupsFor(roster, input)
		if err != nil {
			return policy.SelectionPick{}, err
		}
		groups := make([]rosterdata.Group, 0, len(rankedGroups))
		for _, group := range rankedGroups {
			groups = append(groups, rosterdata.Group(group))
		}
		candidates := make(map[string]struct{}, len(input.CandidateRosterIDs))
		for _, rosterID := range input.CandidateRosterIDs {
			candidates[rosterID] = struct{}{}
		}
		var profile DomainProfile
		if input.TaskDomain != nil && input.TaskDomain.Status == taskdomain.Ready && input.TaskDomain.EvidenceSHA256 == evidenceSHA256 {
			profile = input.TaskDomain.Profile
		}
		selected := selectGroups(roster, catalogID, Request{
			Groups:          groups,
			Harness:         input.Harness,
			LadderKey:       SubscriptionLadder(input.Harness, input.SubscriptionProviders),
			Candidates:      candidates,
			QualityBias:     input.QualityBias,
			PreferredModels: input.PreferredModels,
			Evidence:        evidence,
			Profile:         profile,
		})
		pick := selected.Pick
		if !selected.Found {
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
		logFields := []any{"strategy", input.Strategy, "group", pick.Group, "arm", pick.Arm.String()}
		if input.QualityBias != nil {
			logFields = append(logFields,
				"quality_bias", *input.QualityBias,
				"effective_alpha", EffectiveAlpha(roster, string(pick.Group), *input.QualityBias),
			)
		}
		log.Debug("HMM preference-aware arm selected", logFields...)
		return policy.SelectionPick{
			Group: string(pick.Group), Arm: pick.Arm.String(), ArmScoresByGroup: selected.ScoresByGroup,
			RankedFallback: rankedFallback(roster, rankedGroups, input.Harness, selected.OrdersByGroup, candidates, input.ClassProbabilities), RosterSHA256: roster.SHA256,
			Trace: policy.SelectionTrace{
				TaskDomain:             input.TaskDomain,
				ClassifierRanking:      append([]string(nil), rankedGroups...),
				Harness:                input.Harness,
				ForcedGroup:            input.ForcedGroup,
				CandidateRosterIDs:     append([]string(nil), input.CandidateRosterIDs...),
				QualityBias:            input.QualityBias,
				PreferredModels:        append([]string(nil), input.PreferredModels...),
				SubscriptionProviders:  append([]string(nil), input.SubscriptionProviders...),
				EffectiveOrders:        selected.OrdersByGroup,
				ScoresByGroup:          selected.ScoresByGroup,
				ScoreComponentsByGroup: selected.ComponentsByGroup,
				SelectedGroup:          string(pick.Group),
				SelectedArm:            pick.Arm.String(),
				FallbackDepth:          pick.FallbackDepth,
			},
		}, nil
	}
}

func rankedGroupsFor(roster *rosterdata.Roster, input policy.SelectionInput) ([]string, error) {
	var rankedGroups []string
	var err error
	if input.Unscorable {
		rankedGroups, err = unscorableGroups(roster, input)
	} else {
		rankedGroups, err = classifierGroups(input)
	}
	if err != nil {
		return nil, err
	}
	if input.ForcedGroup != "" {
		if _, exists := roster.Clusters[input.ForcedGroup]; !exists {
			return nil, fmt.Errorf("forced group %q is absent from policy: %w", input.ForcedGroup, ErrNoEligibleArm)
		}
		return []string{input.ForcedGroup}, nil
	}
	if index := slices.Index(rankedGroups, input.LeadGroup); input.LeadGroup != "" && index > 0 {
		rankedGroups = append([]string{input.LeadGroup}, append(rankedGroups[:index], rankedGroups[index+1:]...)...)
	}
	return rankedGroups, nil
}

func rankedFallback(roster *rosterdata.Roster, rankedGroups []string, harness string, ordersByGroup map[string][]string, candidates map[string]struct{}, probabilities map[string]float64) []policy.PreviewGroup {
	fallback := make([]policy.PreviewGroup, 0, len(rankedGroups))
	for _, label := range rankedGroups {
		cluster, exists := roster.Clusters[label]
		if !exists {
			fallback = append(fallback, policy.PreviewGroup{Group: label, Probability: probabilities[label]})
			continue
		}
		harnessOrder, _ := ArmOrder(cluster, harness)
		fallback = append(fallback, policy.PreviewGroup{Group: label, Probability: probabilities[label], RosterArms: append([]string(nil), harnessOrder...), EligibleArms: eligibleOrder(ordersByGroup[label], candidates)})
	}
	return fallback
}

func unscorableGroups(roster *rosterdata.Roster, input policy.SelectionInput) ([]string, error) {
	groups := append([]string(nil), roster.ClassOrder...)
	if len(groups) == 0 {
		for group := range roster.Clusters {
			groups = append(groups, group)
		}
		sort.Strings(groups)
	}
	if input.MinimumGroup != "" && escalation.Rank(input.MinimumGroup) < 0 {
		return nil, ErrNoEligibleArm
	}
	eligible := make([]string, 0, len(groups))
	for _, group := range groups {
		if input.MinimumGroup != "" && escalation.Rank(escalation.Group(group)) < escalation.Rank(input.MinimumGroup) {
			continue
		}
		eligible = append(eligible, group)
	}
	if input.MinimumGroup != "" && len(eligible) == 0 {
		return nil, ErrNoEligibleArm
	}
	if input.PreferredRosterID != "" {
		for index, group := range eligible {
			for _, arm := range roster.Clusters[group].Arms {
				if armid.Parse(arm).Base == input.PreferredRosterID {
					return append([]string{group}, append(eligible[:index], eligible[index+1:]...)...), nil
				}
			}
		}
	}
	return eligible, nil
}

func classifierGroups(input policy.SelectionInput) ([]string, error) {
	if len(input.ClassOrder) == 0 || len(input.ClassProbabilities) != len(input.ClassOrder) {
		return nil, fmt.Errorf("classifier returned an incomplete class contract: %w", ErrInvalidClassifierOutput)
	}
	rank := make(map[string]int, len(input.ClassOrder))
	groups := append([]string(nil), input.ClassOrder...)
	total := 0.0
	for index, label := range input.ClassOrder {
		probability, exists := input.ClassProbabilities[label]
		if label == "" || !exists || math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 || probability > 1 {
			return nil, fmt.Errorf("classifier returned invalid probability for %q: %w", label, ErrInvalidClassifierOutput)
		}
		if _, duplicate := rank[label]; duplicate {
			return nil, fmt.Errorf("classifier returned duplicate class %q: %w", label, ErrInvalidClassifierOutput)
		}
		rank[label] = index
		total += probability
	}
	if math.Abs(total-1) > 1e-6 {
		return nil, fmt.Errorf("classifier probabilities sum to %.9f: %w", total, ErrInvalidClassifierOutput)
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
		if _, exists := candidates[armid.Parse(arm).Base]; exists {
			eligible = append(eligible, arm)
		}
	}
	return eligible
}
