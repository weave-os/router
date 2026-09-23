package policyregistry_test

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

func TestExactRollbackRestoresProfileInventoryAndRetainsSessions(t *testing.T) {
	ctx := context.Background()
	store, controller, initialSet := controllerFixture(t)
	initialProposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, initialSet, servingEpoch)
	initialRef := store.publish(t, policyregistry.ServingProposals, initialProposal)
	initial, err := controller.Activate(ctx, initialRef, "workflow", true)
	require.NoError(t, err)

	profileSet := initialSet
	profileSet.Profiles = maps.Clone(initialSet.Profiles)
	defaultRelease := store.object(t, policyregistry.ServingReleases, initialSet.Default.Release).(*policyregistry.ServingRelease)
	profileSet.Profiles[profileKeyOne] = registerProfileFixture(t, store, initialSet.Default, profileKeyOne, defaultRelease.Policy)
	profileSetRef := store.publish(t, policyregistry.ServingSelectionSets, profileSet)
	profileProposal := fixtureProposal(t, initial.Snapshot, profileSet, servingEpoch)
	profileProposal.Scope = policyregistry.ChangeProfile
	profileProposal.ProfileKey = profileKeyOne
	profileProposal.SourceRelease = profileSet.Profiles[profileKeyOne].Release
	profileRef := store.publish(t, policyregistry.ServingProposals, profileProposal)
	forward, err := controller.Activate(ctx, profileRef, "workflow", true)
	require.NoError(t, err)

	selectionSets := map[string]policyregistry.SelectionSet{initialProposal.SelectionSet.SHA256: initialSet, profileSetRef.SHA256: profileSet}
	projection := policyregistry.AdmissionProjection{Target: initialSet.Target, ProfileKey: profileKeyOne, AssignmentGeneration: 1}
	session, err := policyregistry.SelectSessionRelease(nil, projection, forward.Snapshot, selectionSets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)

	rollbackProposal := fixtureProposal(t, forward.Snapshot, initialSet, servingEpoch)
	forwardRemovalRef := store.publish(t, policyregistry.ServingProposals, rollbackProposal)
	_, err = controller.Prepare(ctx, forwardRemovalRef)
	require.ErrorContains(t, err, "registered profile keys cannot be removed")
	_, err = controller.Activate(ctx, forwardRemovalRef, "workflow", true)
	require.ErrorContains(t, err, "registered profile keys cannot be removed")

	rollbackProposal.Scope = policyregistry.ChangeRollback
	rollbackRef := store.publish(t, policyregistry.ServingProposals, rollbackProposal)
	prepared, err := controller.Prepare(ctx, rollbackRef)
	require.NoError(t, err)
	require.True(t, prepared.Prepared)
	require.Equal(t, forward.Snapshot, store.states[initialSet.Target])
	_, err = controller.Activate(ctx, rollbackRef, "workflow", false)
	require.ErrorContains(t, err, "approval")
	rollback, err := controller.Activate(ctx, rollbackRef, "workflow", true)
	require.NoError(t, err)
	require.Equal(t, initialProposal.SelectionSet, rollback.Activation.SelectionSet)
	require.NotEqual(t, initial.Activation.ID, rollback.Activation.ID)
	require.NotEqual(t, forward.Activation.ID, rollback.Activation.ID)
	require.Greater(t, rollback.Snapshot.Generation, forward.Snapshot.Generation)
	require.Greater(t, forward.Snapshot.Generation, initial.Snapshot.Generation)
	require.Empty(t, rollbackProposal.WithdrawActivations)

	retained, err := policyregistry.SelectSessionRelease(&session, projection, rollback.Snapshot, selectionSets, testRegistryRoot, servingEpoch.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, session.ActivationID, retained.ActivationID)
	require.Equal(t, session.Selection, retained.Selection)
	_, err = policyregistry.SelectSessionRelease(nil, projection, rollback.Snapshot, selectionSets, testRegistryRoot, servingEpoch.Add(time.Minute))
	require.ErrorContains(t, err, "default fallback is forbidden")
	fresh, err := policyregistry.SelectSessionRelease(nil, policyregistry.AdmissionProjection{Target: initialSet.Target}, rollback.Snapshot, selectionSets, testRegistryRoot, servingEpoch.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, rollback.Activation.ID, fresh.ActivationID)

	retry, err := controller.Activate(ctx, profileRef, "workflow", true)
	require.NoError(t, err)
	require.True(t, retry.Replayed)
	require.Equal(t, policyregistry.ActivationSuperseded, retry.Outcome)
	require.Equal(t, rollback.Snapshot, store.states[initialSet.Target])
}

func TestExactRollbackRejectsUnservedSetEvenWithHistoricalSource(t *testing.T) {
	ctx := context.Background()
	store, controller, selectionSet := controllerFixture(t)
	proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, selectionSet, servingEpoch)
	initial, err := controller.Activate(ctx, store.publish(t, policyregistry.ServingProposals, proposal), "workflow", true)
	require.NoError(t, err)
	defaultRelease := store.object(t, policyregistry.ServingReleases, selectionSet.Default.Release).(*policyregistry.ServingRelease)
	selectionSet.Profiles[profileKeyOne] = registerProfileFixture(t, store, selectionSet.Default, profileKeyOne, defaultRelease.Policy)
	store.publish(t, policyregistry.ServingSelectionSets, selectionSet)
	proposal = fixtureProposal(t, initial.Snapshot, selectionSet, servingEpoch)
	proposal.Scope = policyregistry.ChangeRollback
	ref := store.publish(t, policyregistry.ServingProposals, proposal)
	_, err = controller.Prepare(ctx, ref)
	require.ErrorContains(t, err, "selection set previously activated on the same target")
	for _, rollbackOperation := range []func(context.Context, policyregistry.ObjectRef, string, bool) (policyregistry.ActivationResult, error){controller.Activate, controller.Rollback} {
		_, err = rollbackOperation(ctx, ref, "workflow", true)
		require.ErrorContains(t, err, "selection set previously activated on the same target")
	}
	require.Equal(t, initial.Snapshot, store.states[selectionSet.Target])
}

func TestExactRollbackCannotBootstrapAndPreservesValidationAndCAS(t *testing.T) {
	ctx := context.Background()
	store, controller, selectionSet := controllerFixture(t)
	proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, selectionSet, servingEpoch)
	proposal.Scope = policyregistry.ChangeRollback
	require.ErrorContains(t, proposal.Validate(testRegistryRoot), "existing target activation")
	proposal.Scope = policyregistry.ChangeFull
	initial, err := controller.Activate(ctx, store.publish(t, policyregistry.ServingProposals, proposal), "workflow", true)
	require.NoError(t, err)
	proposal = fixtureProposal(t, initial.Snapshot, selectionSet, servingEpoch)
	proposal.Scope = policyregistry.ChangeRollback
	ref := store.publish(t, policyregistry.ServingProposals, proposal)
	store.casErr = policyregistry.ErrConflict
	_, err = controller.Activate(ctx, ref, "workflow", true)
	require.ErrorIs(t, err, policyregistry.ErrConflict)
	store.casErr = nil
	delete(store.artifacts, artifactRef("binding-attestation"))
	_, err = controller.Prepare(ctx, ref)
	require.ErrorContains(t, err, "verify physical revision attestation")
	_, err = controller.Activate(ctx, ref, "workflow", true)
	require.ErrorContains(t, err, "verify physical revision attestation")
	require.Equal(t, initial.Snapshot, store.states[selectionSet.Target])
	store.readErr = errors.New("registry unavailable")
	_, err = controller.Prepare(ctx, ref)
	require.ErrorIs(t, err, store.readErr)
}

func TestExactRollbackBindsHistoricalGenerationAndSource(t *testing.T) {
	for _, mismatch := range []string{"generation", "source", "target"} {
		t.Run(mismatch, func(t *testing.T) {
			ctx := context.Background()
			store, controller, selectionSet := controllerFixture(t)
			initialProposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, selectionSet, servingEpoch)
			initial, err := controller.Activate(ctx, store.publish(t, policyregistry.ServingProposals, initialProposal), "workflow", true)
			require.NoError(t, err)
			proposal := fixtureProposal(t, initial.Snapshot, selectionSet, servingEpoch)
			proposal.Scope = policyregistry.ChangeRollback
			expectedError := "selection set previously activated on the same target"
			switch mismatch {
			case "generation":
				proposal.SelectionSet.Generation++
				store.putRaw(proposal.SelectionSet, servingPayload(t, &selectionSet))
			case "source":
				release := *store.object(t, policyregistry.ServingReleases, selectionSet.Default.Release).(*policyregistry.ServingRelease)
				release.Provenance.RouterRevision = "4444444444444444444444444444444444444444"
				proposal.SourceRelease = store.publish(t, policyregistry.ServingReleases, release)
				expectedError = "exact selected source composition"
			case "target":
				proposal.Target = policyregistry.TargetStaging
				expectedError = "targets differ"
			}
			require.ErrorContains(t, controller.ValidateProposal(ctx, proposal), expectedError)
			require.Equal(t, initial.Snapshot, store.states[selectionSet.Target])
		})
	}
}
