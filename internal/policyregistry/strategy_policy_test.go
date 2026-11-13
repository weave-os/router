package policyregistry_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func TestServingCandidatePinsAnIndependentQwenRoster(t *testing.T) {
	store, _, legacySet := controllerFixture(t)
	fixture := newV2Fixture(t, store, legacySet)
	baseCandidate := store.object(t, policyregistry.ServingCandidate, fixture.candidate).(*policyregistry.CandidateV2)
	strategyPolicy := publishRosterArm(t, store, baseCandidate.Policy, alternateRosterArm)
	candidate := *baseCandidate
	candidate.StrategyPolicies = map[router.Strategy]policyregistry.PolicyObject{router.StrategyHMMQwen: strategyPolicy}
	candidate.Classifier.Identity.Variants = map[router.Strategy]policy.ClassifierArtifact{
		router.StrategyHMMQwen: {
			ArtifactID: "qwen-test", PackageSHA256: strings.Repeat("d", 64),
			Endpoint: "https://qwen-test.modal.run/", Audience: "https://qwen-test.modal.run",
		},
	}
	candidateRef := store.publishArtifact(t, policyregistry.ServingCandidate, candidate)
	require.NoError(t, candidate.Validate(testRegistryRoot))
	defaultLane := fixture.set.Default
	defaultLane.Candidate = candidateRef
	set := policyregistry.SelectionSetV2{
		SchemaVersion: policyregistry.ServingSelectionSetV2,
		Target:        fixture.set.Target,
		Default:       defaultLane,
		Profiles:      map[string]policyregistry.ServingLane{},
	}
	selectionSetRef := store.publishArtifact(t, policyregistry.ServingSelectionSet, set)
	selection := policyregistry.ServingSelection{Release: candidateRef, Binding: selectionSetRef}

	var received policyregistry.Candidate
	cache, err := policyregistry.NewServingRuntimeCache(store, func(_ context.Context, runtime policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		received = runtime
		return map[router.Strategy]router.Router{
			router.StrategyHMM:     stubRouter{},
			router.StrategyHMMQwen: stubRouter{},
		}, nil
	})
	require.NoError(t, err)
	snapshot, err := cache.Snapshot(context.Background(), policyregistry.SessionReleaseBinding{
		Target:    fixture.set.Target,
		Selection: selection,
	})
	require.NoError(t, err)
	require.Equal(t, baseCandidate.Policy, received.Release.Policy)
	qwenPolicy, exists := received.StrategyPolicies[router.StrategyHMMQwen]
	require.True(t, exists)
	require.Equal(t, strategyPolicy.SHA256, qwenPolicy.Reference.SHA256)
	require.Equal(t, []string{alternateRosterArm}, qwenPolicy.Roster.AllArms())
	require.Equal(t, []string{alternateRosterArm}, snapshot.StrategyPolicies[router.StrategyHMMQwen].Roster.AllArms())

	source := policyregistry.AdmittedRosterSource{}
	for _, strategy := range []router.Strategy{router.StrategyHMM, router.StrategyHMMBeta, router.StrategyHMMQwen} {
		t.Run(string(strategy), func(t *testing.T) {
			ctx := router.WithStrategy(policyregistry.WithServingSnapshot(context.Background(), snapshot), strategy)
			expectedRoster, expectedReference := snapshot.Policy, baseCandidate.Policy
			if strategy == router.StrategyHMMQwen {
				expectedRoster, expectedReference = qwenPolicy.Roster, strategyPolicy
			}
			arms, err := source.Roster(ctx)
			require.NoError(t, err)
			require.Equal(t, expectedRoster.AllArms(), arms)
			distribution, err := source.DistributionRoster(ctx)
			require.NoError(t, err)
			require.Same(t, expectedRoster, distribution)
			clusters, err := source.ClusterRoster(ctx)
			require.NoError(t, err)
			require.Equal(t, expectedReference.SHA256, clusters.PolicySHA256)
		})
	}
	require.Equal(t, baseCandidate.Policy, snapshot.Release.Policy, "strategy discovery must not mutate the default snapshot")

	t.Run("missing classifier variant", func(t *testing.T) {
		invalid := candidate
		invalid.Classifier.Identity.Variants = nil
		require.ErrorContains(t, invalid.Validate(testRegistryRoot), "has no classifier variant")
	})
	t.Run("policy identity affects composition", func(t *testing.T) {
		changed := candidate.CandidateComposition
		changed.StrategyPolicies = map[router.Strategy]policyregistry.PolicyObject{router.StrategyHMMQwen: baseCandidate.Policy}
		require.False(t, candidate.CandidateComposition.Equal(changed))
	})
	t.Run("incompatible strategy taxonomy fails closed", func(t *testing.T) {
		incompatible := *qwenPolicy.Roster
		incompatible.ClassOrder = []string{"unknown-class"}
		reference := policyregistry.ObjectRef{URI: strategyPolicy.URI, SHA256: strategyPolicy.SHA256, Generation: strategyPolicy.Generation}
		store.policies[reference] = &incompatible
		_, err := policyregistry.ReadPreparedSelection(context.Background(), store, set.Target, "", selection)
		require.ErrorContains(t, err, "does not match classifier schema and taxonomy")
	})
}
