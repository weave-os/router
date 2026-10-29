package policyregistry_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/subscriptions/entitlement"
)

func TestCohortCustomSelectionRetiresProfileAndRetainsHistory(t *testing.T) {
	ctx := context.Background()
	store, original := cohortFixture(t)
	original.Profiles[entitlement.LegacyBoostProfileKey] = original.Profiles[profileKeyOne]
	delete(original.Profiles, profileKeyOne)
	controller := permissiveController(t, store)
	initialProposal := cohortProposal(t, store, original, nil, policyregistry.ChangeFull, "")
	initial, err := controller.Activate(ctx, store.publishArtifact(t, policyregistry.ServingProposal, initialProposal), "workflow")
	require.NoError(t, err)

	retired := original
	retired.Profiles = maps.Clone(original.Profiles)
	delete(retired.Profiles, entitlement.LegacyBoostProfileKey)
	for _, scope := range []policyregistry.ChangeScope{policyregistry.ChangeFull, policyregistry.ChangeRouter, policyregistry.ChangeRoster, policyregistry.ChangeClassifier} {
		proposal := cohortProposal(t, store, retired, &initial.Activation.SelectionSet, scope, "")
		require.ErrorContains(t, controller.ValidateProposal(ctx, proposal), "registered profile keys cannot be removed")
	}
	custom := cohortProposal(t, store, retired, &initial.Activation.SelectionSet, policyregistry.ChangeCustom, "")
	require.NoError(t, controller.ValidateProposal(ctx, custom))

	changed := retired
	changed.Profiles = maps.Clone(retired.Profiles)
	changed.Profiles[profileKeyTwo] = original.Profiles[entitlement.LegacyBoostProfileKey]
	invalid := cohortProposal(t, store, changed, &initial.Activation.SelectionSet, policyregistry.ChangeCustom, "")
	require.ErrorContains(t, controller.ValidateProposal(ctx, invalid), "retain destination profile revisions")

	activated, err := controller.Activate(ctx, store.publishArtifact(t, policyregistry.ServingProposal, custom), "workflow")
	require.NoError(t, err)
	require.Equal(t, custom.SelectionSet, activated.Activation.SelectionSet)
	require.Contains(t, activated.Snapshot.State.Activations, initial.Activation.ID)
	oldSelection := original.View(initial.Activation.SelectionSet).Profiles[entitlement.LegacyBoostProfileKey]
	historical, err := policyregistry.ReadPreparedSelection(ctx, store, original.Target, entitlement.LegacyBoostProfileKey, oldSelection)
	require.NoError(t, err)
	require.Equal(t, original.Profiles[entitlement.LegacyBoostProfileKey].SHA256, historical.PolicyReference.SHA256)
}

func TestCohortConfigurationScopesRejectCodeOrUnrelatedPolicyChanges(t *testing.T) {
	for _, scope := range []policyregistry.ChangeScope{policyregistry.ChangeRoster, policyregistry.ChangeProfile} {
		t.Run(string(scope), func(t *testing.T) {
			store, original := cohortFixture(t)
			controller := permissiveController(t, store)
			initial := cohortProposal(t, store, original, nil, policyregistry.ChangeFull, "")
			canonical := original
			canonical.Profiles = maps.Clone(original.Profiles)
			profileKey := ""
			if scope == policyregistry.ChangeProfile {
				profileKey = profileKeyOne
				canonical.Profiles[profileKey] = original.Profiles[profileKeyTwo]
			} else {
				canonical.DefaultPolicy = original.Profiles[profileKeyTwo]
			}
			proposal := cohortProposal(t, store, canonical, &initial.SelectionSet, scope, profileKey)
			require.NoError(t, controller.ValidateProposal(context.Background(), proposal))
			for name, mutate := range map[string]func(*policyregistry.SelectionSetV3){
				"revision":      func(s *policyregistry.SelectionSetV3) { s.Code.Router.Name = "different-worker" },
				"configuration": func(s *policyregistry.SelectionSetV3) { s.Code.Router.Configuration = artifactRef("different-config") },
				"candidate": func(s *policyregistry.SelectionSetV3) {
					candidate := *store.object(t, policyregistry.ServingCandidate, s.Code.Candidate).(*policyregistry.CandidateV2)
					candidate.Policy = original.Profiles[profileKeyTwo]
					s.Code.Candidate = store.publishArtifact(t, policyregistry.ServingCandidate, candidate)
				},
				"other customer":   func(s *policyregistry.SelectionSetV3) { s.Profiles[profileKeyTwo] = original.Profiles[profileKeyOne] },
				"removed customer": func(s *policyregistry.SelectionSetV3) { delete(s.Profiles, profileKeyTwo) },
				"registered customer": func(s *policyregistry.SelectionSetV3) {
					s.Profiles["10000000-0000-4000-8000-000000000003"] = s.DefaultPolicy
				},
			} {
				t.Run(name, func(t *testing.T) {
					invalid := canonical
					invalid.Profiles = maps.Clone(canonical.Profiles)
					mutate(&invalid)
					proposal := cohortProposal(t, store, invalid, &initial.SelectionSet, scope, profileKey)
					require.Error(t, controller.ValidateProposal(context.Background(), proposal))
				})
			}
		})
	}
}

func TestCohortCodeComponentChangesPreserveEffectivePolicies(t *testing.T) {
	for _, scope := range []policyregistry.ChangeScope{policyregistry.ChangeRouter, policyregistry.ChangeClassifier} {
		t.Run(string(scope), func(t *testing.T) {
			store, initial := cohortFixture(t)
			// Default is deliberately different from the candidate's bootstrap policy too.
			initial.DefaultPolicy = initial.Profiles[profileKeyTwo]
			initialRef := store.publishArtifact(t, policyregistry.ServingSelectionSet, initial)
			next := initial
			next.Profiles = maps.Clone(initial.Profiles)
			candidate := *store.object(t, policyregistry.ServingCandidate, initial.Code.Candidate).(*policyregistry.CandidateV2)
			if scope == policyregistry.ChangeRouter {
				candidate.RouterImageDigest = "sha256:" + strings.Repeat("4", 64)
				candidate.Provenance.RouterRevision = strings.Repeat("4", 40)
				next.Code.Router.Name = "worker-0002"
				next.Code.Router.ImageDigest = candidate.RouterImageDigest
			} else {
				candidate.Classifier.Identity.ArtifactID = "synthetic-next-classifier"
				next.Code.Classifier.Name = "classifier-0002"
			}
			next.Code.Candidate = store.publishArtifact(t, policyregistry.ServingCandidate, candidate)
			proposal := cohortProposal(t, store, next, &initialRef, scope, "")
			controller := permissiveController(t, store)
			require.NoError(t, controller.ValidateProposal(context.Background(), proposal))
			for _, profile := range []string{"", profileKeyTwo} {
				changed := next
				changed.Profiles = maps.Clone(next.Profiles)
				if profile == "" {
					changed.DefaultPolicy = initial.Profiles[profileKeyOne]
				} else {
					changed.Profiles[profile] = initial.Profiles[profileKeyOne]
				}
				proposal.SelectionSet = store.publishArtifact(t, policyregistry.ServingSelectionSet, changed)
				require.Error(t, controller.ValidateProposal(context.Background(), proposal), "code-only scopes cannot replace effective rosters with bootstrap policy")
			}
		})
	}
}

func TestCohortWorkerColdStartAndDestinationValidationUseEveryAssignedPolicy(t *testing.T) {
	store, set := cohortFixture(t)
	set.DefaultPolicy = set.Profiles[profileKeyTwo]
	ref := store.publishArtifact(t, policyregistry.ServingSelectionSet, set)
	identity := policyregistry.WorkerIdentity{Target: set.Target, Project: set.Code.Project, Region: set.Code.Region, Revision: set.Code.Router.Name, ImageDigest: set.Code.Router.ImageDigest, Configuration: set.Code.Router.Configuration}
	cache := cohortCache(t, store)
	baseline, err := cache.PrepareWorker(context.Background(), identity, ref)
	require.NoError(t, err)
	require.Equal(t, []string{alternateRosterArm}, baseline.Policy.AllArms())
	for key, selection := range set.View(ref).Profiles {
		attestation, err := policyregistry.ValidateWorkerSelection(context.Background(), store, cache, identity, policyregistry.WorkerValidationRequest{Target: set.Target, ProfileKey: key, Selection: selection})
		require.NoError(t, err)
		require.True(t, attestation.Ready)
		require.Equal(t, selection, attestation.Selection)
		if key == profileKeyTwo {
			require.Equal(t, []string{alternateRosterArm}, attestation.CatalogArms)
		}
	}
	missing := set.Profiles[profileKeyOne]
	delete(store.policies, policyregistry.ObjectRef{URI: missing.URI, SHA256: missing.SHA256, Generation: missing.Generation})
	_, err = cache.PrepareWorker(context.Background(), identity, ref)
	require.ErrorIs(t, err, policyregistry.ErrNotFound, "cold start must not hide an unavailable customer roster")
}

func expandCohortV2(t *testing.T, store *servingMemoryStore, set policyregistry.SelectionSetV3) policyregistry.SelectionSetV2 {
	t.Helper()
	candidate := *store.object(t, policyregistry.ServingCandidate, set.Code.Candidate).(*policyregistry.CandidateV2)
	lane := func(key string, policy policyregistry.PolicyObject) policyregistry.ServingLane {
		configured := candidate
		configured.Policy = policy
		selection := policyregistry.ServingLane{Candidate: store.publishArtifact(t, policyregistry.ServingCandidate, configured), LaneBinding: set.Code.LaneBinding}
		if key != "" {
			selection.ProfileKey = key
			selection.ProfilePolicy = &policy
			requirements := candidate.Requirements
			selection.ProfileRequirements = &requirements
		}
		return selection
	}
	v2 := policyregistry.SelectionSetV2{SchemaVersion: policyregistry.ServingSelectionSetV2, Target: set.Target, Default: lane("", set.DefaultPolicy), Profiles: map[string]policyregistry.ServingLane{}}
	for key, policy := range set.Profiles {
		v2.Profiles[key] = lane(key, policy)
	}
	return v2
}

func TestCohortV3AndV2TransitionsCompareEffectivePolicies(t *testing.T) {
	store, initial := cohortFixture(t)
	initial.DefaultPolicy = initial.Profiles[profileKeyTwo]
	v3Ref := store.publishArtifact(t, policyregistry.ServingSelectionSet, initial)
	v2 := expandCohortV2(t, store, initial)
	v2Ref := store.publishArtifact(t, policyregistry.ServingSelectionSet, v2)
	controller := permissiveController(t, store)
	// V3 -> v2 preserves the configured default, even though its candidate differs from the v3 build candidate.
	proposal := cohortProposal(t, store, initial, &v3Ref, policyregistry.ChangeFull, "")
	proposal.SelectionSet, proposal.SourceCandidate = v2Ref, v2.Default.Candidate
	require.NoError(t, controller.ValidateProposal(context.Background(), proposal))

	// A code-only upgrade can compact v2 derived candidates into one v3 candidate while retaining every policy.
	candidate := *store.object(t, policyregistry.ServingCandidate, initial.Code.Candidate).(*policyregistry.CandidateV2)
	candidate.RouterImageDigest = "sha256:" + strings.Repeat("7", 64)
	candidate.Provenance.RouterRevision = strings.Repeat("7", 40)
	next := initial
	next.Code.Candidate = store.publishArtifact(t, policyregistry.ServingCandidate, candidate)
	next.Code.Router.Name = "worker-0007"
	next.Code.Router.ImageDigest = candidate.RouterImageDigest
	proposal = cohortProposal(t, store, next, &v2Ref, policyregistry.ChangeRouter, "")
	require.NoError(t, controller.ValidateProposal(context.Background(), proposal))
	next.DefaultPolicy = initial.Profiles[profileKeyOne]
	proposal.SelectionSet = store.publishArtifact(t, policyregistry.ServingSelectionSet, next)
	require.ErrorContains(t, controller.ValidateProposal(context.Background(), proposal), "retain destination policy")
}

func TestCohortRosterRollbackKeepsNewerCodeCandidate(t *testing.T) {
	store, original := cohortFixture(t)
	controller := permissiveController(t, store)
	apply := func(set policyregistry.SelectionSetV3, previous *policyregistry.ObjectRef, scope policyregistry.ChangeScope) policyregistry.ActivationResult {
		t.Helper()
		proposal := cohortProposal(t, store, set, previous, scope, "")
		activation, err := controller.Activate(context.Background(), store.publishArtifact(t, policyregistry.ServingProposal, proposal), "workflow")
		require.NoError(t, err)
		return activation
	}
	initial := apply(original, nil, policyregistry.ChangeFull)
	updated := original
	updated.DefaultPolicy = original.Profiles[profileKeyTwo]
	rosterUpdate := apply(updated, &initial.Activation.SelectionSet, policyregistry.ChangeRoster)
	candidate := *store.object(t, policyregistry.ServingCandidate, original.Code.Candidate).(*policyregistry.CandidateV2)
	candidate.RouterImageDigest = "sha256:" + strings.Repeat("8", 64)
	candidate.Provenance.RouterRevision = strings.Repeat("8", 40)
	updated.Code.Candidate = store.publishArtifact(t, policyregistry.ServingCandidate, candidate)
	updated.Code.Router.Name = "worker-0008"
	updated.Code.Router.ImageDigest = candidate.RouterImageDigest
	codeUpdate := apply(updated, &rosterUpdate.Activation.SelectionSet, policyregistry.ChangeRouter)
	restored := updated
	restored.DefaultPolicy = original.DefaultPolicy
	rollback := apply(restored, &codeUpdate.Activation.SelectionSet, policyregistry.ChangeRoster)
	view := restored.View(rollback.Activation.SelectionSet)
	prepared, err := policyregistry.ReadPreparedSelection(context.Background(), store, restored.Target, "", view.Default)
	require.NoError(t, err)
	require.Equal(t, original.DefaultPolicy, prepared.PolicyReference)
	require.Equal(t, candidate.RouterImageDigest, prepared.Candidate.RouterImageDigest)
	require.Equal(t, "worker-0008", prepared.Binding.Router.Name)
	require.NotEqual(t, original.Code.Candidate, view.Default.Release)
	require.Equal(t, updated.Code.Candidate, view.Default.Release)
	require.Nil(t, rollback.Snapshot.State.Activations[codeUpdate.Activation.ID].WithdrawnAt)
}
