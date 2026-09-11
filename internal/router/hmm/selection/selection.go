// Package selection owns the HMM strategies' deterministic within-cluster arm
// selection (harness order, rank-1 pick, ranked cluster-fallback walk). It serves
// whenever a declarative roster is configured; see docs/HMM_GO_SELECTION.md.
package selection

import (
	"math"
	"sort"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/rosterdata"
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

// Group is one classifier group ranked from raw probabilities in Go.
type Group struct {
	Label string
}

// ArmOrder returns the harness-specific arm order when the roster declares a non-empty one, else the pooled order (private-sidecar arms_by_harness extension).
// Roster harness keys use underscores (claude_code) while router.Request.ClientApp is hyphenated (claude-code), so both spellings are tried.
func ArmOrder(cluster rosterdata.Cluster, harness string) (order []string, harnessSpecific bool) {
	exactHarness := rosterdata.Harness(harness)
	if arms := cluster.ArmsByHarness[exactHarness]; len(arms) > 0 {
		return arms, true
	}
	normalizedHarness := rosterdata.Harness(strings.ReplaceAll(harness, "-", "_"))
	if arms := cluster.ArmsByHarness[normalizedHarness]; len(arms) > 0 {
		return arms, true
	}
	return cluster.Arms, false
}

// Select returns the first arm from rankedGroups (pre-sorted desc probability, sidecar's
// ranked_fallback order) whose base ID is in candidates. The private sidecar additionally
// clamps by mode/turn-type and filters via membership_by_harness; neither is applied here.
func Select(roster *rosterdata.Roster, rankedGroups []string, harness string, candidates map[string]struct{}) (Pick, bool) {
	groups := make([]Group, 0, len(rankedGroups))
	for _, label := range rankedGroups {
		groups = append(groups, Group{Label: label})
	}
	return SelectGroups(roster, groups, harness, candidates)
}

// SelectGroups walks Go-ranked groups against the router's hard-eligible candidates.
func SelectGroups(roster *rosterdata.Roster, groups []Group, harness string, candidates map[string]struct{}) (Pick, bool) {
	pick, _, ok := SelectGroupsWithPreference(roster, groups, harness, candidates, nil)
	return pick, ok
}

// SelectGroupsWithPreference applies quality/price ranking inside each
// classifier group after all hard eligibility filters. The classifier's group
// order is never changed. Returned score maps are grouped because a later
// force-cluster or key override may change the final group.
func SelectGroupsWithPreference(roster *rosterdata.Roster, groups []Group, harness string, candidates map[string]struct{}, qualityBias *float64) (Pick, map[string]map[string]float32, bool) {
	pick, scoresByGroup, _, _, ok := SelectGroupsWithPreferences(roster, groups, harness, candidates, qualityBias, nil, nil, nil)
	return pick, scoresByGroup, ok
}

// SelectGroupsWithPreferences applies Go-owned score preferences after hard
// eligibility and returns each group's effective order for diagnostics.
func SelectGroupsWithPreferences(
	roster *rosterdata.Roster,
	groups []Group,
	harness string,
	candidates map[string]struct{},
	qualityBias *float64,
	preferredModels []string,
	subscriptionStatePreferredModels []string,
	subsidizedModelCostFactor map[string]float64,
) (Pick, map[string]map[string]float32, map[string]map[string]router.SelectionScoreComponents, map[string][]string, bool) {
	scoresByGroup := make(map[string]map[string]float32, len(groups))
	componentsByGroup := make(map[string]map[string]router.SelectionScoreComponents, len(groups))
	ordersByGroup := make(map[string][]string, len(groups))
	hasPreferenceInputs := len(preferredModels) > 0 || len(subscriptionStatePreferredModels) > 0 || len(subsidizedModelCostFactor) > 0
	for _, group := range groups {
		if cluster, ok := roster.Clusters[group.Label]; ok {
			scores, components := scoresWithPreferences(roster, group.Label, cluster, qualityBias, preferredModels, subscriptionStatePreferredModels, subsidizedModelCostFactor)
			scoresByGroup[group.Label] = scores
			componentsByGroup[group.Label] = components
			order, _ := ArmOrder(cluster, harness)
			ordersByGroup[group.Label] = preferenceOrder(roster, group.Label, cluster, harness, order, qualityBias, hasPreferenceInputs, scores)
		}
	}
	depth := 0
	for _, group := range groups {
		cluster, ok := roster.Clusters[group.Label]
		if !ok {
			// A ranked label absent from the roster contributes no arms; the
			// sidecar walks it the same way (clusters.get(label) or {}).
			depth++
			continue
		}
		_, harnessSpecific := ArmOrder(cluster, harness)
		order := ordersByGroup[group.Label]
		for _, arm := range order {
			// Candidates carry base roster IDs, so effort-suffixed arms
			// (model:high) match on their base ID.
			baseID, _ := hmm.SplitEffort(arm)
			if _, eligible := candidates[baseID]; !eligible {
				continue
			}
			return Pick{Group: group.Label, Arm: arm, FallbackDepth: depth, HarnessOrder: harnessSpecific}, scoresByGroup, componentsByGroup, ordersByGroup, true
		}
		depth++
	}
	return Pick{}, scoresByGroup, componentsByGroup, ordersByGroup, false
}

// Scores returns the fixed neutral scores or preference-adjusted WII/WPI score
// for every indexed arm in a cluster.
func Scores(roster *rosterdata.Roster, label string, cluster rosterdata.Cluster, qualityBias *float64) map[string]float32 {
	neutral := roster.Ranking.QualityBiasNeutral
	if qualityBias == nil || *qualityBias == neutral || !isDynamicRoster(roster) {
		scores := make(map[string]float32, len(cluster.ArmScores))
		for arm, score := range cluster.ArmScores {
			scores[arm] = float32(score)
		}
		return scores
	}
	alpha := EffectiveAlpha(roster, label, *qualityBias)
	scores := make(map[string]float32, len(cluster.ArmIndices))
	for arm, indices := range cluster.ArmIndices {
		scores[arm] = float32(alpha*indices.WII - (1-alpha)*indices.WPI)
	}
	return scores
}

// EffectiveAlpha maps the user dial piecewise around the neutral point so the
// neutral UI setting reproduces each cluster's independently tuned alpha.
func EffectiveAlpha(roster *rosterdata.Roster, label string, qualityBias float64) float64 {
	qualityBias = math.Max(0, math.Min(1, qualityBias))
	neutral := roster.Ranking.QualityBiasNeutral
	defaultAlpha := roster.Ranking.Alpha[label]
	if qualityBias <= neutral {
		return roster.Ranking.AlphaMin[label] + (defaultAlpha-roster.Ranking.AlphaMin[label])*(qualityBias/neutral)
	}
	return defaultAlpha + (roster.Ranking.AlphaMax[label]-defaultAlpha)*((qualityBias-neutral)/(1-neutral))
}

func preferenceOrder(roster *rosterdata.Roster, label string, cluster rosterdata.Cluster, harness string, order []string, qualityBias *float64, hasPreferenceInputs bool, scores map[string]float32) []string {
	if (qualityBias == nil || *qualityBias == roster.Ranking.QualityBiasNeutral || !isDynamicRoster(roster)) && !hasPreferenceInputs {
		return order
	}
	pins := append([]string(nil), cluster.ManualPinsByHarness[rosterdata.HarnessAll]...)
	pins = append(pins, harnessList(cluster.ManualPinsByHarness, harness)...)
	preferredVendors := harnessList(cluster.PreferredVendorsByHarness, harness)
	pinPosition := make(map[string]int, len(pins))
	for index, arm := range pins {
		pinPosition[arm] = index
	}
	positions := make(map[string]int, len(order))
	for index, arm := range order {
		positions[arm] = index
	}
	ranked := append([]string(nil), order...)
	sort.SliceStable(ranked, func(i, j int) bool {
		left, right := ranked[i], ranked[j]
		leftPin, leftPinned := pinPosition[left]
		rightPin, rightPinned := pinPosition[right]
		if leftPinned != rightPinned {
			return leftPinned
		}
		if leftPinned && leftPin != rightPin {
			return leftPin < rightPin
		}
		leftVendor := vendorRank(left, preferredVendors)
		rightVendor := vendorRank(right, preferredVendors)
		if leftVendor != rightVendor {
			return leftVendor < rightVendor
		}
		leftScore, leftScored := scores[left]
		rightScore, rightScored := scores[right]
		if leftScored != rightScored {
			return leftScored
		}
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return positions[left] < positions[right]
	})
	return ranked
}

func scoresWithPreferences(
	roster *rosterdata.Roster,
	label string,
	cluster rosterdata.Cluster,
	qualityBias *float64,
	preferredModels []string,
	subscriptionStatePreferredModels []string,
	subsidizedModelCostFactor map[string]float64,
) (map[string]float32, map[string]router.SelectionScoreComponents) {
	scores := Scores(roster, label, cluster, qualityBias)
	preferredModelBonus := roster.Preferences.PreferredModelBonus
	if preferredModelBonus == 0 {
		preferredModelBonus = 0.5
	}
	subscriptionBonus := roster.Preferences.SubscriptionBonus
	if subscriptionBonus == 0 {
		subscriptionBonus = 0.35
	}
	preferredRanks := preferenceRanks(preferredModels)
	subscriptionStateRanks := preferenceRanks(subscriptionStatePreferredModels)
	componentsByArm := make(map[string]router.SelectionScoreComponents, len(scores))
	for arm, score := range scores {
		baseID, _ := hmm.SplitEffort(arm)
		catalogID := hmm.CatalogIDForRoster(baseID)
		components := router.SelectionScoreComponents{BaseScore: score}
		if rank, preferred := preferredRanks[catalogID]; preferred {
			components.PreferredModelBonus = float32(preferredModelBonus / float64(rank+1))
		}
		if rank, preferred := subscriptionStateRanks[catalogID]; preferred {
			components.SubscriptionStateBonus = float32(subscriptionBonus / float64(rank+1))
		}
		if factor, subsidized := subsidizedModelCostFactor[catalogID]; subsidized && factor > 0 {
			boundedFactor := math.Min(factor, 1)
			components.SubscriptionCostBonus = float32(subscriptionBonus * (1 - boundedFactor))
		}
		components.TotalScore = components.BaseScore + components.PreferredModelBonus + components.SubscriptionStateBonus + components.SubscriptionCostBonus
		scores[arm] = components.TotalScore
		componentsByArm[arm] = components
	}
	return scores, componentsByArm
}

func preferenceRanks(models []string) map[string]int {
	ranks := make(map[string]int, len(models))
	for rank, model := range models {
		if _, exists := ranks[model]; !exists {
			ranks[model] = rank
		}
	}
	return ranks
}

func harnessList(values map[rosterdata.Harness][]string, harness string) []string {
	if exact := values[rosterdata.Harness(harness)]; len(exact) > 0 {
		return exact
	}
	return values[rosterdata.Harness(strings.ReplaceAll(harness, "-", "_"))]
}

func vendorRank(arm string, preferred []string) int {
	vendor := strings.SplitN(arm, "/", 2)[0]
	for index, candidate := range preferred {
		if vendor == candidate {
			return index
		}
	}
	return len(preferred)
}

func isDynamicRoster(roster *rosterdata.Roster) bool {
	return roster.SchemaVersion == rosterdata.SchemaVersionV7 || roster.SchemaVersion == rosterdata.SchemaVersionV75C || roster.SchemaVersion == rosterdata.SchemaVersionPolicyV1
}
