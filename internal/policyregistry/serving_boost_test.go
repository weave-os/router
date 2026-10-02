package policyregistry_test

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/subscriptions/entitlement"
)

func TestBoostRosterMovesAtomicallyWithDefault(t *testing.T) {
	store, original := cohortFixture(t)
	boost, ok := entitlement.ServingProfileFor(entitlement.PlanBoost)
	require.True(t, ok)
	original.Profiles[boost.Key] = original.DefaultPolicy
	controller := permissiveController(t, store)
	initial := cohortProposal(t, store, original, nil, policyregistry.ChangeFull, "")
	require.NoError(t, controller.ValidateProposal(context.Background(), initial))

	updated := original
	updated.Profiles = maps.Clone(original.Profiles)
	updated.DefaultPolicy = original.Profiles[profileKeyTwo]
	updated.Profiles[boost.Key] = updated.DefaultPolicy
	proposal := cohortProposal(t, store, updated, &initial.SelectionSet, policyregistry.ChangeRoster, "")
	require.NoError(t, controller.ValidateProposal(context.Background(), proposal))

	for name, mutate := range map[string]func(*policyregistry.SelectionSetV3){
		"stale boost":       func(set *policyregistry.SelectionSetV3) { set.Profiles[boost.Key] = original.DefaultPolicy },
		"unrelated profile": func(set *policyregistry.SelectionSetV3) { set.Profiles[profileKeyOne] = updated.DefaultPolicy },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := updated
			invalid.Profiles = maps.Clone(updated.Profiles)
			mutate(&invalid)
			proposal := cohortProposal(t, store, invalid, &initial.SelectionSet, policyregistry.ChangeRoster, "")
			require.Error(t, controller.ValidateProposal(context.Background(), proposal))
		})
	}
}

func TestBoostCannotActivateAnIndependentRoster(t *testing.T) {
	for _, scope := range []policyregistry.ChangeScope{policyregistry.ChangeFull, policyregistry.ChangeProfile, policyregistry.ChangeRollback} {
		t.Run(string(scope), func(t *testing.T) {
			store, original := cohortFixture(t)
			boost, ok := entitlement.ServingProfileFor(entitlement.PlanBoost)
			require.True(t, ok)
			original.Profiles[boost.Key] = original.DefaultPolicy
			initial := cohortProposal(t, store, original, nil, policyregistry.ChangeFull, "")
			invalid := original
			invalid.Profiles = maps.Clone(original.Profiles)
			invalid.Profiles[boost.Key] = original.Profiles[profileKeyTwo]
			profileKey := ""
			if scope == policyregistry.ChangeProfile {
				profileKey = boost.Key
			}
			proposal := cohortProposal(t, store, invalid, &initial.SelectionSet, scope, profileKey)
			require.ErrorContains(t, permissiveController(t, store).ValidateProposal(context.Background(), proposal), "Boost must use the default roster")
		})
	}
}

func TestBoostRosterMovesAtomicallyWithDefaultV2(t *testing.T) {
	store, _, original := controllerFixture(t)
	baseRelease := *store.object(t, policyregistry.ServingReleases, original.Default.Release).(*policyregistry.ServingRelease)
	boost, ok := entitlement.ServingProfileFor(entitlement.PlanBoost)
	require.True(t, ok)
	original.Profiles[boost.Key] = registerProfileFixture(t, store, original.Default, boost.Key, baseRelease.Policy)
	original.Profiles[profileKeyTwo] = registerProfileFixture(t, store, original.Default, profileKeyTwo, baseRelease.Policy)
	_, previous := foldSelectionSet(t, store, original)
	changedPolicy := publishRosterArm(t, store, baseRelease.Policy, alternateRosterArm)
	baseRelease.Policy = changedPolicy
	binding := store.object(t, policyregistry.ServingBindings, original.Default.Binding).(*policyregistry.DeploymentBinding)
	updated := original
	updated.Profiles = maps.Clone(original.Profiles)
	updated.Default = publishSelection(t, store, original.Default, baseRelease, binding.Router, binding.Classifier, nil)
	updated.Profiles[boost.Key] = registerProfileFixture(t, store, updated.Default, boost.Key, changedPolicy)
	_, next := foldSelectionSet(t, store, updated)
	proposal := policyregistry.DeploymentProposalV2{
		SchemaVersion: policyregistry.ServingProposalV2, Target: updated.Target, PreviousSelectionSet: &previous,
		SelectionSet: next, SourceCandidate: foldCandidate(t, store, updated.Default.Release), Scope: policyregistry.ChangeRoster,
		Actor: "test-operator", Reason: "shared Boost roster", RequestID: "synthetic-boost-release", CreatedAt: servingEpoch,
		Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{},
	}
	controller := permissiveController(t, store)
	require.NoError(t, controller.ValidateProposal(context.Background(), proposal))
	customProposal := proposal
	customProposal.Scope = policyregistry.ChangeCustom
	require.NoError(t, controller.ValidateProposal(context.Background(), customProposal))

	tampered := updated
	tampered.Profiles = maps.Clone(updated.Profiles)
	boostSelection := tampered.Profiles[boost.Key]
	boostBinding := *store.object(t, policyregistry.ServingBindings, boostSelection.Binding).(*policyregistry.DeploymentBinding)
	boostBinding.Attestation = artifactRef("evidence")
	tampered.Profiles[boost.Key] = policyregistry.ServingSelection{
		Release: boostSelection.Release,
		Binding: store.publish(t, policyregistry.ServingBindings, boostBinding),
		Profile: boostSelection.Profile,
	}
	_, tamperedSelection := foldSelectionSet(t, store, tampered)
	tamperedProposal := proposal
	tamperedProposal.SelectionSet = tamperedSelection
	require.ErrorContains(t, controller.ValidateProposal(context.Background(), tamperedProposal), "roster-only promotion changes Boost outside its policy")

	tamperedCandidate := updated
	tamperedCandidate.Profiles = maps.Clone(updated.Profiles)
	boostSelection = tamperedCandidate.Profiles[boost.Key]
	boostRelease := *store.object(t, policyregistry.ServingReleases, boostSelection.Release).(*policyregistry.ServingRelease)
	boostRelease.Provenance.BuildAttestation = artifactRef("evidence")
	tamperedCandidate.Profiles[boost.Key] = publishSelection(t, store, boostSelection, boostRelease, binding.Router, binding.Classifier, boostSelection.Profile)
	_, tamperedCandidateRef := foldSelectionSet(t, store, tamperedCandidate)
	tamperedCandidateProposal := proposal
	tamperedCandidateProposal.SelectionSet = tamperedCandidateRef
	require.ErrorContains(t, controller.ValidateProposal(context.Background(), tamperedCandidateProposal), "roster-only promotion changes Boost outside its policy")

	updated.Profiles[boost.Key] = original.Profiles[boost.Key]
	_, stale := foldSelectionSet(t, store, updated)
	proposal.SelectionSet = stale
	require.ErrorContains(t, controller.ValidateProposal(context.Background(), proposal), "Boost must use the default roster")
}
