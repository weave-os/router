package selection_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
)

func distributionWiredProviders() map[string]struct{} {
	return map[string]struct{}{
		providers.ProviderAnthropic: {}, providers.ProviderAnthropicGateway: {},
		providers.ProviderOpenAIGateway: {}, providers.ProviderOpenAI: {}, providers.ProviderXAI: {},
	}
}

func TestRoutingDistributionUsesLivePreferenceScorer(t *testing.T) {
	roster := &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionV7,
		Ranking: rosterdata.Ranking{
			Alpha:              map[string]float64{"low": 0.4},
			AlphaMin:           map[string]float64{"low": 0.05},
			AlphaMax:           map[string]float64{"low": 0.8},
			QualityBiasNeutral: 0.7,
		},
		Clusters: map[string]rosterdata.Cluster{
			"low": {
				Arms: []string{"openai/gpt-5.6-luna", "x-ai/grok-4.6"},
				ArmScores: map[string]float64{
					"openai/gpt-5.6-luna": 30,
					"x-ai/grok-4.6":       25,
				},
				ArmIndices: map[string]rosterdata.ArmIndices{
					"openai/gpt-5.6-luna": {WII: 90, WPI: 10},
					"x-ai/grok-4.6":       {WII: 55, WPI: 0},
				},
			},
		},
	}

	points, err := selection.RoutingDistribution(roster, 3, distributionWiredProviders(), nil, nil)
	require.NoError(t, err)
	require.Len(t, points, 3)
	require.Len(t, points[0].Models, 1)
	require.Len(t, points[2].Models, 1)
	assert.NotEqual(t, points[0].Models[0].Model, points[2].Models[0].Model)
	assert.Equal(t, 1.0, points[0].Models[0].Share)
	assert.Positive(t, points[0].ProjectedCostPer1KInputUSD)
	roster.SchemaVersion = rosterdata.SchemaVersionPolicyV1
	compiledPoints, err := selection.RoutingDistribution(roster, 3, distributionWiredProviders(), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, points, compiledPoints)
}

func TestRoutingDistributionUsesRemainingProviderBinding(t *testing.T) {
	roster := &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionPolicyV1,
		Ranking: rosterdata.Ranking{
			Alpha: map[string]float64{"low": 0.5}, AlphaMin: map[string]float64{"low": 0.1},
			AlphaMax: map[string]float64{"low": 0.9}, QualityBiasNeutral: 0.7,
		},
		Clusters: map[string]rosterdata.Cluster{"low": {
			Arms:       []string{"anthropic/claude-haiku-4.5"},
			ArmScores:  map[string]float64{"anthropic/claude-haiku-4.5": 1},
			ArmIndices: map[string]rosterdata.ArmIndices{"anthropic/claude-haiku-4.5": {WII: 1, WPI: 1}},
		}},
	}
	points, err := selection.RoutingDistribution(roster, 2, distributionWiredProviders(), nil, map[string]struct{}{providers.ProviderAnthropic: {}})
	require.NoError(t, err)
	require.Len(t, points, 2)
	assert.Equal(t, "claude-haiku-4-5", points[0].Models[0].Model)
	assert.Equal(t, 0.001, points[0].ProjectedCostPer1KInputUSD)

	_, err = selection.RoutingDistribution(roster, 2, distributionWiredProviders(), nil, map[string]struct{}{
		providers.ProviderAnthropic: {}, providers.ProviderAnthropicGateway: {}, providers.ProviderOpenAIGateway: {},
	})
	require.Error(t, err)
}

func TestRoutingDistributionMatchesServingCandidateUniverse(t *testing.T) {
	const retiredArm = "openai/gpt-5.5"
	const servingArm = "anthropic/claude-haiku-4.5"
	roster := &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionPolicyV1,
		Ranking: rosterdata.Ranking{
			Alpha: map[string]float64{"low": 0.5}, AlphaMin: map[string]float64{"low": 0.1},
			AlphaMax: map[string]float64{"low": 0.9}, QualityBiasNeutral: 0.7,
		},
		Clusters: map[string]rosterdata.Cluster{"low": {
			Arms:       []string{retiredArm, servingArm},
			ArmScores:  map[string]float64{retiredArm: 100, servingArm: 0},
			ArmIndices: map[string]rosterdata.ArmIndices{retiredArm: {WII: 100, WPI: 0}, servingArm: {WII: 0, WPI: 100}},
		}},
	}
	wiredProviders := map[string]struct{}{
		providers.ProviderOpenAI: {}, providers.ProviderAnthropic: {}, providers.ProviderAnthropicGateway: {},
	}
	requestProviders := map[string]struct{}{
		providers.ProviderOpenAI: {}, providers.ProviderAnthropicGateway: {},
	}
	resolver := policy.NewResolver(catalog.HMMRoutingTargetSet(wiredProviders), wiredProviders, armid.ForModel, policy.ManagedProviderPolicy())
	resolved := resolver.Resolve(router.Request{EnabledProviders: requestProviders})
	candidateRosterIDs := make([]string, 0, len(resolved.Candidates))
	for _, candidate := range resolved.Candidates {
		candidateRosterIDs = append(candidateRosterIDs, candidate.RosterID)
	}
	assert.NotContains(t, candidateRosterIDs, retiredArm)
	assert.Contains(t, candidateRosterIDs, servingArm)
	assert.Equal(t, providers.ProviderAnthropicGateway, resolved.CandidateProviders()["claude-haiku-4-5"])

	qualityBias := 1.0
	pick, err := selection.Selector(roster)(context.Background(), policy.SelectionInput{
		ClassOrder: []string{"low"}, ClassProbabilities: map[string]float64{"low": 1},
		CandidateRosterIDs: candidateRosterIDs, QualityBias: &qualityBias,
	})
	require.NoError(t, err)
	assert.Equal(t, servingArm, pick.Arm)

	points, err := selection.RoutingDistribution(roster, 2, wiredProviders, nil, map[string]struct{}{providers.ProviderAnthropic: {}})
	require.NoError(t, err)
	require.Len(t, points, 2)
	assert.Equal(t, []cluster.ModelShare{{Model: "claude-haiku-4-5", Share: 1}}, points[1].Models)
	assert.Equal(t, 0.001, points[1].ProjectedCostPer1KInputUSD)
}

func TestRoutingDistributionRejectsUnwiredCatalogBindings(t *testing.T) {
	roster := &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionPolicyV1,
		Ranking: rosterdata.Ranking{
			Alpha: map[string]float64{"low": 0.5}, AlphaMin: map[string]float64{"low": 0.1},
			AlphaMax: map[string]float64{"low": 0.9}, QualityBiasNeutral: 0.7,
		},
		Clusters: map[string]rosterdata.Cluster{"low": {
			Arms:       []string{"anthropic/claude-haiku-4.5"},
			ArmScores:  map[string]float64{"anthropic/claude-haiku-4.5": 1},
			ArmIndices: map[string]rosterdata.ArmIndices{"anthropic/claude-haiku-4.5": {WII: 1, WPI: 1}},
		}},
	}

	_, err := selection.RoutingDistribution(
		roster,
		2,
		map[string]struct{}{providers.ProviderAnthropic: {}},
		nil,
		map[string]struct{}{providers.ProviderAnthropic: {}},
	)
	require.ErrorIs(t, err, cluster.ErrNoEligibleProvider)
}

func TestRoutingDistributionHonorsExclusions(t *testing.T) {
	roster := dynamicRoster()
	// Replace test-only IDs with catalog-backed IDs so distribution can resolve them.
	cluster := roster.Clusters["low"]
	cluster.Arms = []string{"openai/gpt-5.6-luna", "x-ai/grok-4.6"}
	cluster.ArmScores = map[string]float64{"openai/gpt-5.6-luna": 30, "x-ai/grok-4.6": 25}
	cluster.ArmIndices = map[string]rosterdata.ArmIndices{
		"openai/gpt-5.6-luna": {WII: 90, WPI: 10},
		"x-ai/grok-4.6":       {WII: 55, WPI: 0},
	}
	roster.Clusters["low"] = cluster

	points, err := selection.RoutingDistribution(
		roster,
		2,
		distributionWiredProviders(),
		map[string]struct{}{"gpt-5.6-luna": {}},
		nil,
	)
	require.NoError(t, err)
	for _, point := range points {
		require.Len(t, point.Models, 1)
		assert.NotEqual(t, "gpt-5.6-luna", point.Models[0].Model)
	}
}

func TestRoutingDistributionBoundsGridBeforeAllocation(t *testing.T) {
	_, err := selection.RoutingDistribution(dynamicRoster(), 1_000_000_000, nil, nil, nil)
	require.ErrorContains(t, err, "grid exceeds 101 points")
}
