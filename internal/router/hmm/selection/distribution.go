package selection

import (
	"fmt"
	"sort"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

const defaultDistributionGrid = 21
const maxDistributionGrid = 101

// RoutingDistribution projects the HMM roster's within-band model mix across
// the quality/price dial. Each classifier band contributes equal weight; live
// traffic weights remain request-dependent and are intentionally not guessed.
func RoutingDistribution(roster *rosterdata.Roster, gridN int, availableProviders, excludedModels, excludedProviders map[string]struct{}) ([]cluster.DistributionPoint, error) {
	if roster == nil || !isDynamicRoster(roster) {
		return nil, fmt.Errorf("HMM routing distribution requires a dynamic roster or compiled serving policy")
	}
	if gridN < 2 {
		gridN = defaultDistributionGrid
	}
	if gridN > maxDistributionGrid {
		return nil, fmt.Errorf("HMM routing distribution grid exceeds %d points", maxDistributionGrid)
	}
	labels := make([]string, 0, len(roster.Clusters))
	for label := range roster.Clusters {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	routingTargets := catalog.HMMRoutingTargetSet(availableProviders)
	points := make([]cluster.DistributionPoint, 0, gridN)
	for gridIndex := 0; gridIndex < gridN; gridIndex++ {
		qualityBias := float64(gridIndex) / float64(gridN-1)
		counts := make(map[string]int)
		prices := make(map[string]float64)
		selectedGroups := 0
		for _, label := range labels {
			clusterRoster := roster.Clusters[label]
			candidates := make(map[string]struct{}, len(clusterRoster.Arms))
			for _, arm := range clusterRoster.Arms {
				baseRosterID, _ := hmm.SplitEffort(arm)
				catalogID := hmm.CatalogIDForRoster(baseRosterID)
				binding, ok := eligibleBinding(catalogID, routingTargets, availableProviders, excludedModels, excludedProviders)
				if !ok {
					continue
				}
				candidates[baseRosterID] = struct{}{}
				prices[catalogID] = binding.Price.InputUSDPer1M / 1000
			}
			pick, _, ok := SelectGroupsWithPreference(
				roster,
				[]Group{{Label: label}},
				"",
				candidates,
				&qualityBias,
			)
			if !ok {
				continue
			}
			selectedGroups++
			counts[hmm.CatalogIDForRoster(pick.Arm)]++
		}
		if len(counts) == 0 {
			return nil, fmt.Errorf("exclusions leave no eligible candidates: %w", cluster.ErrNoEligibleProvider)
		}
		modelShares := make([]cluster.ModelShare, 0, len(counts))
		projectedCost := 0.0
		for model, count := range counts {
			share := float64(count) / float64(selectedGroups)
			modelShares = append(modelShares, cluster.ModelShare{Model: model, Share: share})
			projectedCost += share * prices[model]
		}
		sort.Slice(modelShares, func(i, j int) bool {
			if modelShares[i].Share != modelShares[j].Share {
				return modelShares[i].Share > modelShares[j].Share
			}
			return modelShares[i].Model < modelShares[j].Model
		})
		points = append(points, cluster.DistributionPoint{
			QualityBias:                qualityBias,
			Models:                     modelShares,
			ProjectedCostPer1KInputUSD: projectedCost,
		})
	}
	return points, nil
}

// EligibleBinding returns the first policy-allowed catalog binding that survives
// the same model and provider exclusions used by managed request selection.
func EligibleBinding(catalogID string, availableProviders, excludedModels, excludedProviders map[string]struct{}) (catalog.ProviderBinding, bool) {
	return eligibleBinding(catalogID, catalog.HMMRoutingTargetSet(availableProviders), availableProviders, excludedModels, excludedProviders)
}

func eligibleBinding(catalogID string, routingTargets, availableProviders, excludedModels, excludedProviders map[string]struct{}) (catalog.ProviderBinding, bool) {
	if _, excluded := excludedModels[catalogID]; excluded {
		return catalog.ProviderBinding{}, false
	}
	_, ok := routingTargets[catalogID]
	if !ok {
		return catalog.ProviderBinding{}, false
	}
	providerPolicy := policy.ManagedProviderPolicy()
	for _, binding := range catalog.EnumerateBindings(catalogID, availableProviders) {
		if _, excluded := excludedProviders[binding.Provider]; !excluded && providerPolicy.Allows(binding.Provider) {
			return binding.ProviderBinding, true
		}
	}
	return catalog.ProviderBinding{}, false
}
