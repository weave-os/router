package selection

import (
	"errors"
	"fmt"
	"sort"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selectionpolicy"
)

const defaultDistributionGrid = 21

var errNoDistributionPolicy = errors.New("HMM routing distribution requires a dynamic roster or compiled serving policy")

func RoutingDistribution(admitted *selectionpolicy.Policy, gridN int, excludedModels, excludedProviders map[string]struct{}) ([]router.DistributionPoint, error) {
	if admitted == nil {
		return nil, errNoDistributionPolicy
	}
	return routingDistribution(admitted.Roster(), admitted.CatalogID, gridN, excludedModels, excludedProviders)
}

func routingDistribution(roster *rosterdata.Roster, catalogIDForArm func(string) string, gridN int, excludedModels, excludedProviders map[string]struct{}) ([]router.DistributionPoint, error) {
	if roster == nil || !roster.SchemaVersion.IsGoSelectionSchema() {
		return nil, errNoDistributionPolicy
	}
	if gridN < 2 {
		gridN = defaultDistributionGrid
	}
	labels := make([]string, 0, len(roster.Clusters))
	for label := range roster.Clusters {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	points := make([]router.DistributionPoint, 0, gridN)
	for gridIndex := 0; gridIndex < gridN; gridIndex++ {
		qualityBias := float64(gridIndex) / float64(gridN-1)
		counts := make(map[string]int)
		prices := make(map[string]float64)
		selectedGroups := 0
		for _, label := range labels {
			candidates := distributionCandidates(roster.Clusters[label], catalogIDForArm, excludedModels, excludedProviders, prices)
			selected := selectGroups(roster, catalogIDForArm, Request{Groups: []rosterdata.Group{rosterdata.Group(label)}, Candidates: candidates, QualityBias: &qualityBias})
			if !selected.Found {
				continue
			}
			selectedGroups++
			counts[catalogIDForArm(selected.Pick.Arm.String())]++
		}
		if len(counts) == 0 {
			return nil, fmt.Errorf("exclusions leave no eligible candidates: %w", router.ErrNoEligibleProvider)
		}
		modelShares := make([]router.ModelShare, 0, len(counts))
		projectedCost := 0.0
		for model, count := range counts {
			share := float64(count) / float64(selectedGroups)
			modelShares = append(modelShares, router.ModelShare{Model: model, Share: share})
			projectedCost += share * prices[model]
		}
		sort.Slice(modelShares, func(i, j int) bool {
			if modelShares[i].Share != modelShares[j].Share {
				return modelShares[i].Share > modelShares[j].Share
			}
			return modelShares[i].Model < modelShares[j].Model
		})
		points = append(points, router.DistributionPoint{
			QualityBias:                qualityBias,
			Models:                     modelShares,
			ProjectedCostPer1KInputUSD: projectedCost,
		})
	}
	return points, nil
}

func distributionCandidates(cluster rosterdata.Cluster, catalogIDForArm func(string) string, excludedModels, excludedProviders map[string]struct{}, prices map[string]float64) map[string]struct{} {
	candidates := make(map[string]struct{}, len(cluster.Arms))
	for _, arm := range cluster.Arms {
		baseRosterID := armid.Parse(arm).Base
		catalogID := catalogIDForArm(baseRosterID)
		model, ok := catalog.ByID(catalogID)
		if !ok {
			continue
		}
		if _, excluded := excludedModels[catalogID]; excluded {
			continue
		}
		if _, excluded := excludedProviders[model.PrimaryProvider()]; excluded {
			continue
		}
		candidates[baseRosterID] = struct{}{}
		if len(model.Providers) > 0 {
			prices[catalogID] = model.Providers[0].Price.InputUSDPer1M / 1000
		}
	}
	return candidates
}
