package policyregistry_test

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

func TestExactRollbackRestoresProfileInventoryAndRetainsSessions(t *testing.T) {
	ctx := context.Background()
	store, controller, initialSet := controllerFixture(t)
	initialProposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, initialSet, servingEpoch)
	initialRef := store.publishArtifact(t, policyregistry.ServingProposal, initialProposal)
	initial, err := controller.Activate(ctx, initialRef, "workflow")
	require.NoError(t, err)

	profileSet := initialSet
	profileSet.Profiles = maps.Clone(initialSet.Profiles)
	defaultRelease := store.object(t, policyregistry.ServingReleases, initialSet.Default.Release).(*policyregistry.ServingRelease)
	profileSet.Profiles[profileKeyOne] = registerProfileFixture(t, store, initialSet.Default, profileKeyOne, defaultRelease.Policy)
	store.publish(t, policyregistry.ServingSelectionSets, profileSet)
	profileProposal := storedProposal(t, store, initial.Snapshot, profileSet, servingEpoch)
	profileProposal.Scope = policyregistry.ChangeProfile
	profileProposal.ProfileKey = profileKeyOne
	profileProposal.SourceCandidate = foldCandidate(t, store, profileSet.Profiles[profileKeyOne].Release)
	profileRef := store.publishArtifact(t, policyregistry.ServingProposal, profileProposal)
	forward, err := controller.Activate(ctx, profileRef, "workflow")
	require.NoError(t, err)

	selectionSets := storedViews(t, store, initialProposal.SelectionSet, profileProposal.SelectionSet)
	projection := policyregistry.AdmissionProjection{Target: initialSet.Target, ProfileKey: profileKeyOne, AssignmentGeneration: 1}
	session, err := policyregistry.SelectSessionRelease(nil, projection, forward.Snapshot, selectionSets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)

	rollbackProposal := storedProposal(t, store, forward.Snapshot, initialSet, servingEpoch)
	forwardRemovalRef := store.publishArtifact(t, policyregistry.ServingProposal, rollbackProposal)
	_, err = controller.Prepare(ctx, forwardRemovalRef)
	require.ErrorContains(t, err, "registered profile keys cannot be removed")
	_, err = controller.Activate(ctx, forwardRemovalRef, "workflow")
	require.ErrorContains(t, err, "registered profile keys cannot be removed")

	rollbackProposal.Scope = policyregistry.ChangeRollback
	rollbackRef := store.publishArtifact(t, policyregistry.ServingProposal, rollbackProposal)
	prepared, err := controller.Prepare(ctx, rollbackRef)
	require.NoError(t, err)
	require.True(t, prepared.Prepared)
	require.Equal(t, forward.Snapshot, store.states[initialSet.Target])
	// apply, not a rollback verb, commits the rollback-scoped proposal.
	rollback, err := controller.Activate(ctx, rollbackRef, "workflow")
	require.NoError(t, err)
	require.Equal(t, initialProposal.SelectionSet, rollback.Activation.SelectionSet)
	require.Equal(t, forward.Activation.SelectionSet, *rollbackProposal.PreviousSelectionSet)
	require.NotNil(t, rollback.Snapshot.State.Activations[forward.Activation.ID].SupersededAt)
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

	retry, err := controller.Activate(ctx, profileRef, "workflow")
	require.NoError(t, err)
	require.True(t, retry.Replayed)
	require.Equal(t, policyregistry.ActivationSuperseded, retry.Outcome)
	require.Equal(t, rollback.Snapshot, store.states[initialSet.Target])
}

func TestExactRollbackRejectsUnservedSetEvenWithHistoricalSource(t *testing.T) {
	ctx := context.Background()
	store, controller, selectionSet := controllerFixture(t)
	proposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, selectionSet, servingEpoch)
	initial, err := controller.Activate(ctx, store.publishArtifact(t, policyregistry.ServingProposal, proposal), "workflow")
	require.NoError(t, err)
	defaultRelease := store.object(t, policyregistry.ServingReleases, selectionSet.Default.Release).(*policyregistry.ServingRelease)
	selectionSet.Profiles[profileKeyOne] = registerProfileFixture(t, store, selectionSet.Default, profileKeyOne, defaultRelease.Policy)
	store.publish(t, policyregistry.ServingSelectionSets, selectionSet)
	proposal = storedProposal(t, store, initial.Snapshot, selectionSet, servingEpoch)
	proposal.Scope = policyregistry.ChangeRollback
	ref := store.publishArtifact(t, policyregistry.ServingProposal, proposal)
	_, err = controller.Prepare(ctx, ref)
	require.ErrorContains(t, err, "selection set previously activated on the same target")
	_, err = controller.Activate(ctx, ref, "workflow")
	require.ErrorContains(t, err, "selection set previously activated on the same target")
	require.Equal(t, initial.Snapshot, store.states[selectionSet.Target])
}

func TestExactRollbackCannotBootstrapAndPreservesValidationAndCAS(t *testing.T) {
	ctx := context.Background()
	store, controller, selectionSet := controllerFixture(t)
	proposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, selectionSet, servingEpoch)
	proposal.Scope = policyregistry.ChangeRollback
	require.ErrorContains(t, proposal.Validate(testRegistryRoot), "existing target activation")
	proposal.Scope = policyregistry.ChangeFull
	initial, err := controller.Activate(ctx, store.publishArtifact(t, policyregistry.ServingProposal, proposal), "workflow")
	require.NoError(t, err)
	proposal = storedProposal(t, store, initial.Snapshot, selectionSet, servingEpoch)
	proposal.Scope = policyregistry.ChangeRollback
	ref := store.publishArtifact(t, policyregistry.ServingProposal, proposal)
	store.casErr = policyregistry.ErrConflict
	_, err = controller.Activate(ctx, ref, "workflow")
	require.ErrorIs(t, err, policyregistry.ErrConflict)
	store.casErr = nil
	delete(store.artifacts, artifactRef("binding-attestation"))
	_, err = controller.Prepare(ctx, ref)
	require.ErrorContains(t, err, "verify physical revision attestation")
	_, err = controller.Activate(ctx, ref, "workflow")
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
			initialProposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, selectionSet, servingEpoch)
			initial, err := controller.Activate(ctx, store.publishArtifact(t, policyregistry.ServingProposal, initialProposal), "workflow")
			require.NoError(t, err)
			proposal := storedProposal(t, store, initial.Snapshot, selectionSet, servingEpoch)
			proposal.Scope = policyregistry.ChangeRollback
			expectedError := "selection set previously activated on the same target"
			switch mismatch {
			case "generation":
				folded, _ := foldSelectionSet(t, store, selectionSet)
				proposal.SelectionSet.Generation++
				store.putRaw(proposal.SelectionSet, servingPayload(t, folded))
			case "source":
				release := *store.object(t, policyregistry.ServingReleases, selectionSet.Default.Release).(*policyregistry.ServingRelease)
				release.Provenance.RouterRevision = "4444444444444444444444444444444444444444"
				proposal.SourceCandidate = foldCandidate(t, store, store.publish(t, policyregistry.ServingReleases, release))
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

// legacyV1Snapshot seeds the state a target activated before the layout floor: a v1 selection set
// activated by a v1 proposal, which only history, admission and withdrawal may still read.
func legacyV1Snapshot(t *testing.T, store *servingMemoryStore, set policyregistry.SelectionSet) (policyregistry.ServingStateSnapshot, policyregistry.ObjectRef) {
	t.Helper()
	setRef := store.publish(t, policyregistry.ServingSelectionSets, set)
	proposal := policyregistry.DeploymentProposal{SchemaVersion: policyregistry.ServingProposalV1, Target: set.Target, SelectionSet: setRef, SourceRelease: set.Default.Release, Scope: policyregistry.ChangeFull, Actor: "legacy-operator", Reason: "activation predating the layout floor", RequestID: "legacy-run:lane-0", CreatedAt: servingEpoch, Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{}}
	require.NoError(t, proposal.Validate(testRegistryRoot))
	ref := store.publish(t, policyregistry.ServingProposals, proposal)
	id := uuid.NewString()
	state := policyregistry.ServingControlState{SchemaVersion: policyregistry.ServingControlStateV1, Target: set.Target, CurrentActivationID: id, Sequence: 1, Activations: map[string]policyregistry.Activation{id: {ID: id, Sequence: 1, SelectionSet: setRef, ActivatedAt: servingEpoch, Proposal: ref, RequestID: proposal.RequestID, Actor: proposal.Actor, WorkflowActor: "legacy-workflow"}}}
	require.NoError(t, state.Validate(testRegistryRoot, set.Target))
	snapshot := policyregistry.ServingStateSnapshot{State: state, Generation: 1}
	store.states[set.Target] = snapshot
	return snapshot, setRef
}

// A rollback to the v1 selection set a target really served fails closed on the layout floor,
// while that activation stays readable for in-flight sessions and withdrawable.
func TestLayoutFloorRefusesRollbackToAServedV1SelectionSet(t *testing.T) {
	ctx := context.Background()
	store, controller, initialSet := controllerFixture(t)
	legacy, legacySetRef := legacyV1Snapshot(t, store, initialSet)
	legacyID := legacy.State.CurrentActivationID
	sets := map[string]policyregistry.SelectionSetView{legacySetRef.SHA256: initialSet.View()}
	projection := policyregistry.AdmissionProjection{Target: initialSet.Target}
	session, err := policyregistry.SelectSessionRelease(nil, projection, legacy, sets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)
	require.Equal(t, legacyID, session.ActivationID)

	// Only the newly served set is floored: the incumbent binding still names the v1 set exactly.
	forwardProposal := storedProposal(t, store, legacy, initialSet, servingEpoch)
	require.Equal(t, legacySetRef, *forwardProposal.PreviousSelectionSet)
	require.Contains(t, forwardProposal.SelectionSet.URI, "/artifacts/")
	forward, err := controller.Activate(ctx, store.publishArtifact(t, policyregistry.ServingProposal, forwardProposal), "workflow")
	require.NoError(t, err)
	require.NotNil(t, forward.Snapshot.State.Activations[legacyID].SupersededAt)

	rollback := storedProposal(t, store, forward.Snapshot, initialSet, servingEpoch)
	rollback.Scope = policyregistry.ChangeRollback
	rollback.SelectionSet = legacySetRef
	_, err = controller.Activate(ctx, store.publishArtifact(t, policyregistry.ServingProposal, rollback), "workflow")
	require.ErrorIs(t, err, policyregistry.ErrSelectionSetLayoutFloor)
	require.ErrorContains(t, err, legacySetRef.SHA256)
	require.Equal(t, forward.Snapshot, store.states[initialSet.Target])

	maps.Copy(sets, storedViews(t, store, forwardProposal.SelectionSet))
	retained, err := policyregistry.SelectSessionRelease(&session, projection, forward.Snapshot, sets, testRegistryRoot, servingEpoch.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, legacyID, retained.ActivationID)
	require.Equal(t, session.Selection, retained.Selection)

	withdrawal := storedProposal(t, store, forward.Snapshot, initialSet, servingEpoch)
	withdrawal.WithdrawActivations = []string{legacyID}
	withdrawn, err := controller.Activate(ctx, store.publishArtifact(t, policyregistry.ServingProposal, withdrawal), "workflow")
	require.NoError(t, err)
	require.NotNil(t, withdrawn.Snapshot.State.Activations[legacyID].WithdrawnAt)
	require.Equal(t, withdrawn.Activation.ID, withdrawn.Snapshot.State.Activations[legacyID].ReplacementID)
}
