package selection

import (
	"math"
	"sort"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
)

type Pick struct {
	Group         rosterdata.Group
	Arm           string
	FallbackDepth int
	HarnessOrder  bool
}

func ArmOrder(cluster rosterdata.Cluster, harness string) (order []string, harnessSpecific bool) {
	exactHarness := rosterdata.Harness(harness)
	if arms := cluster.ArmsByHarness[exactHarness]; len(arms) > 0 {
		return arms, true
	}
	normalizedHarness := rosterdata.SelectionHarness(harness)
	if arms := cluster.ArmsByHarness[normalizedHarness]; len(arms) > 0 {
		return arms, true
	}
	return cluster.Arms, false
}

func SelectGroupsWithSubscriptions(
	roster *rosterdata.Roster,
	groups []rosterdata.Group,
	harness string,
	subscriptionProviders []string,
	candidates map[string]struct{},
	qualityBias *float64,
	preferredModels []string,
	evidence *DomainEvidence,
	profile DomainProfile,
) (Pick, map[string]map[string]float32, map[string]map[string]router.SelectionScoreComponents, map[string][]string, bool) {
	return selectGroups(roster, groups, harness, SubscriptionLadder(harness, subscriptionProviders), candidates, qualityBias, preferredModels, evidence, profile)
}

func selectGroups(
	roster *rosterdata.Roster,
	groups []rosterdata.Group,
	harness string,
	ladderKey string,
	candidates map[string]struct{},
	qualityBias *float64,
	preferredModels []string,
	evidence *DomainEvidence,
	profile DomainProfile,
) (Pick, map[string]map[string]float32, map[string]map[string]router.SelectionScoreComponents, map[string][]string, bool) {
	if evidence != nil && len(ScoredDomains(profile)) > 0 && validateDomainEvidence(evidence, roster) != nil {
		evidence = nil
	}
	scoresByGroup := make(map[string]map[string]float32, len(groups))
	componentsByGroup := make(map[string]map[string]router.SelectionScoreComponents, len(groups))
	ordersByGroup := make(map[string][]string, len(groups))
	hasPreferenceInputs := len(preferredModels) > 0
	for _, group := range groups {
		label := string(group)
		if cluster, ok := roster.Clusters[label]; ok {
			scores, components, domainActive := scoresWithDomainPreferences(roster, label, cluster, qualityBias, preferredModels, evidence, profile)
			scoresByGroup[label] = scores
			componentsByGroup[label] = components
			order, _ := ArmOrder(cluster, harness)
			order = preferenceOrder(roster, cluster, harness, order, qualityBias, hasPreferenceInputs || domainActive, scores)
			ordersByGroup[label] = leadWith(order, roster.SubscriptionLadders[ladderKey][group])
		}
	}
	depth := 0
	for _, group := range groups {
		cluster, ok := roster.Clusters[string(group)]
		if !ok {
			depth++
			continue
		}
		_, harnessSpecific := ArmOrder(cluster, harness)
		order := ordersByGroup[string(group)]
		for _, arm := range order {
			if _, eligible := candidates[armid.Parse(arm).Base]; !eligible {
				continue
			}
			return Pick{Group: group, Arm: arm, FallbackDepth: depth, HarnessOrder: harnessSpecific}, scoresByGroup, componentsByGroup, ordersByGroup, true
		}
		depth++
	}
	return Pick{}, scoresByGroup, componentsByGroup, ordersByGroup, false
}

func Scores(roster *rosterdata.Roster, label string, cluster rosterdata.Cluster, qualityBias *float64) map[string]float32 {
	neutral := roster.Ranking.QualityBiasNeutral
	if qualityBias == nil || *qualityBias == neutral || !roster.SchemaVersion.IsGoSelectionSchema() {
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

func EffectiveAlpha(roster *rosterdata.Roster, label string, qualityBias float64) float64 {
	qualityBias = math.Max(0, math.Min(1, qualityBias))
	neutral := roster.Ranking.QualityBiasNeutral
	defaultAlpha := roster.Ranking.Alpha[label]
	if qualityBias <= neutral {
		return roster.Ranking.AlphaMin[label] + (defaultAlpha-roster.Ranking.AlphaMin[label])*(qualityBias/neutral)
	}
	return defaultAlpha + (roster.Ranking.AlphaMax[label]-defaultAlpha)*((qualityBias-neutral)/(1-neutral))
}

func preferenceOrder(roster *rosterdata.Roster, cluster rosterdata.Cluster, harness string, order []string, qualityBias *float64, hasPreferenceInputs bool, scores map[string]float32) []string {
	if (qualityBias == nil || *qualityBias == roster.Ranking.QualityBiasNeutral || !roster.SchemaVersion.IsGoSelectionSchema()) && !hasPreferenceInputs {
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

func scoresWithDomainPreferences(
	roster *rosterdata.Roster,
	label string,
	cluster rosterdata.Cluster,
	qualityBias *float64,
	preferredModels []string,
	evidence *DomainEvidence,
	profile DomainProfile,
) (map[string]float32, map[string]router.SelectionScoreComponents, bool) {
	scores := Scores(roster, label, cluster, qualityBias)
	corrections := make(map[string]float32)
	domainActive := false
	if domains := ScoredDomains(profile); evidence != nil && len(domains) > 0 {
		alpha := roster.Ranking.Alpha[label]
		if qualityBias != nil {
			alpha = EffectiveAlpha(roster, label, *qualityBias)
		}
		for arm, score := range scores {
			correction := float32(alpha * DomainInfluence * TaskQualityDelta(domains, evidence.Arms[arm]))
			corrections[arm] = correction
			scores[arm] = score + correction
			domainActive = domainActive || scores[arm] != score
		}
	}
	preferredModelBonus := roster.Preferences.PreferredModelBonus
	if preferredModelBonus == 0 {
		preferredModelBonus = 0.5
	}
	preferredRanks := preferenceRanks(preferredModels)
	componentsByArm := make(map[string]router.SelectionScoreComponents, len(scores))
	for arm, score := range scores {
		catalogID := armid.CatalogIDForRoster(armid.Parse(arm).Base)
		components := router.SelectionScoreComponents{BaseScore: score - corrections[arm], TaskDomainCorrection: corrections[arm]}
		if rank, preferred := preferredRanks[catalogID]; preferred {
			components.PreferredModelBonus = float32(preferredModelBonus / float64(rank+1))
		}
		components.TotalScore = score + components.PreferredModelBonus
		scores[arm] = components.TotalScore
		componentsByArm[arm] = components
	}
	return scores, componentsByArm, domainActive
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
	return values[rosterdata.SelectionHarness(harness)]
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
