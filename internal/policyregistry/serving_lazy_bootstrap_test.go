package policyregistry_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

// A target whose state still lives under runtime_state/ migrates on its first apply: the controller
// reads the legacy object, then creates state/<env>/<target>.json with DoesNotExist. Later applies
// CAS on the new object's generation and the legacy object is never rewritten.
func TestServingControllerLazilyBootstrapsStatePathFromLegacyState(t *testing.T) {
	ctx := context.Background()
	store, controller, initialSet := controllerFixture(t)
	legacy, legacyProposal := activateFixture(t, policyregistry.ServingStateSnapshot{}, initialSet, servingEpoch)
	store.publish(t, policyregistry.ServingProposals, legacyProposal)
	legacy.Generation = 7
	store.legacyStates[initialSet.Target] = legacy
	legacyBefore, err := json.Marshal(legacy.State)
	require.NoError(t, err)

	observed, err := store.ReadServingState(ctx, initialSet.Target)
	require.NoError(t, err)
	require.True(t, observed.LegacyPath)
	require.EqualValues(t, 7, observed.Generation)

	for _, shape := range proposalShapes {
		t.Run(string(shape), func(t *testing.T) {
			store.legacyStates[initialSet.Target] = legacy
			delete(store.states, initialSet.Target)
			store.casCalls = 0

			firstProposal := fixtureProposal(t, observed, initialSet, servingEpoch)
			require.EqualValues(t, 7, firstProposal.ExpectedGeneration, "v1 proposals still transcribe the legacy generation operators observed")
			_, firstRef := publishShaped(t, store, shape, firstProposal)
			first, err := controller.Activate(ctx, firstRef, "workflow")
			require.NoError(t, err)
			require.False(t, first.Snapshot.LegacyPath)
			require.EqualValues(t, 1, first.Snapshot.Generation, "the new path is created with DoesNotExist, not CASed on the legacy generation")
			require.Equal(t, first.Snapshot, store.states[initialSet.Target])
			require.Equal(t, legacy, store.legacyStates[initialSet.Target])

			// A second operator who also read the legacy object must not overwrite the migration.
			_, err = store.CompareAndSwapServingState(ctx, legacy.State, observed.WriteGeneration())
			require.ErrorIs(t, err, policyregistry.ErrConflict)

			current, err := store.ReadServingState(ctx, initialSet.Target)
			require.NoError(t, err)
			require.Equal(t, first.Snapshot, current)
			secondProposal := fixtureProposal(t, current, initialSet, servingEpoch)
			_, secondRef := publishShaped(t, store, shape, secondProposal)
			second, err := controller.Activate(ctx, secondRef, "workflow")
			require.NoError(t, err)
			require.EqualValues(t, 2, second.Snapshot.Generation)
			require.Equal(t, first.Activation.ID, second.Snapshot.State.Activations[first.Activation.ID].ID)

			stale := fixtureProposal(t, observed, initialSet, servingEpoch)
			_, staleRef := publishShaped(t, store, shape, stale)
			_, err = controller.Activate(ctx, staleRef, "workflow")
			require.ErrorIs(t, err, policyregistry.ErrConflict, "a proposal frozen against the legacy snapshot (v1: generation, v2: previous_selection_set) cannot apply to the migrated path")
			require.Equal(t, legacy, store.legacyStates[initialSet.Target])
			legacyAfter, err := json.Marshal(store.legacyStates[initialSet.Target].State)
			require.NoError(t, err)
			require.Equal(t, legacyBefore, legacyAfter)
		})
	}
}

func TestGCSCompareAndSwapServingStateMigratesLegacyStateToNewPathOnce(t *testing.T) {
	registry, fixture := newServingGCSFixture(t)
	ctx := context.Background()
	set := fixtureSet("active")
	legacySnapshot, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	firstSnapshot, _ := activateFixture(t, legacySnapshot, fixtureSet("first"), servingEpoch.Add(time.Hour))
	secondSnapshot, _ := activateFixture(t, firstSnapshot, fixtureSet("second"), servingEpoch.Add(2*time.Hour))
	legacyName := "weave_registry/runtime_state/router_serving/v1/targets/prod-01/prod/stable/state.json"
	newName := "weave_registry/state/prod-01/prod/stable.json"
	legacyPayload, err := json.Marshal(legacySnapshot.State)
	require.NoError(t, err)
	fixture.mu.Lock()
	fixture.generation = 41
	fixture.objects[legacyName] = map[int64][]byte{41: legacyPayload}
	fixture.current[legacyName] = 41
	fixture.mu.Unlock()

	observed, err := registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.NoError(t, err)
	require.True(t, observed.LegacyPath)
	require.EqualValues(t, 41, observed.Generation)
	require.EqualValues(t, 0, observed.WriteGeneration())

	_, err = registry.CompareAndSwapServingState(ctx, firstSnapshot.State, observed.Generation)
	require.ErrorIs(t, err, policyregistry.ErrConflict, "the legacy generation never matches the not-yet-created new object")

	migrated, err := registry.CompareAndSwapServingState(ctx, firstSnapshot.State, observed.WriteGeneration())
	require.NoError(t, err)
	require.False(t, migrated.LegacyPath)
	fixture.mu.Lock()
	assert.Equal(t, fixture.current[newName], migrated.Generation)
	assert.Equal(t, map[int64][]byte{41: legacyPayload}, fixture.objects[legacyName], "the legacy object is neither rewritten nor removed")
	assert.Len(t, fixture.objects[newName], 1)
	fixture.mu.Unlock()

	_, err = registry.CompareAndSwapServingState(ctx, firstSnapshot.State, 0)
	require.ErrorIs(t, err, policyregistry.ErrConflict, "a concurrent legacy reader cannot bootstrap twice")

	current, err := registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.NoError(t, err)
	require.Equal(t, migrated, current)
	advanced, err := registry.CompareAndSwapServingState(ctx, secondSnapshot.State, current.WriteGeneration())
	require.NoError(t, err)
	require.Greater(t, advanced.Generation, migrated.Generation)

	_, err = registry.CompareAndSwapServingState(ctx, secondSnapshot.State, migrated.Generation)
	require.ErrorIs(t, err, policyregistry.ErrConflict)
	fixture.mu.Lock()
	assert.Equal(t, map[int64][]byte{41: legacyPayload}, fixture.objects[legacyName])
	assert.Len(t, fixture.objects[newName], 2)
	fixture.mu.Unlock()
	final, err := registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.NoError(t, err)
	assert.Equal(t, policyregistry.ServingStateSnapshot{State: secondSnapshot.State, Generation: advanced.Generation}, final)
}

// v2 proposals carry no expected_generation, so previous_selection_set alone must decide whether a
// proposal is a bootstrap: naming an incumbent on an empty target cannot slip a non-full scope in as
// the first activation.
func TestActivationRejectsV2ProposalBindingIncumbentOnEmptyTarget(t *testing.T) {
	ctx := context.Background()
	store, controller, set := controllerFixture(t)
	fixture := newV2Fixture(t, store, set)
	phantom := namespaceRef(policyregistry.ServingSelectionSets, "never-activated")
	proposal := fixtureProposalV2(fixture, &phantom)
	proposal.Scope = policyregistry.ChangeRoster
	require.NoError(t, proposal.Validate(testRegistryRoot))

	_, err := policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, servingPayload(t, proposal), store.publishArtifact(t, policyregistry.ServingProposal, proposal), testRegistryRoot, "workflow", servingEpoch)
	require.ErrorIs(t, err, policyregistry.ErrConflict)
	assert.ErrorContains(t, err, "no activation")

	_, err = controller.Activate(ctx, store.publishArtifact(t, policyregistry.ServingProposal, proposal), "workflow")
	require.ErrorIs(t, err, policyregistry.ErrConflict)
	_, err = store.ReadServingState(ctx, set.Target)
	require.ErrorIs(t, err, policyregistry.ErrNotFound, "a rejected bootstrap must not create target state")

	bootstrap := fixtureProposalV2(fixture, nil)
	activation, err := controller.Activate(ctx, store.publishArtifact(t, policyregistry.ServingProposal, bootstrap), "workflow")
	require.NoError(t, err)
	assert.Equal(t, fixture.setRef, activation.Activation.SelectionSet)
}
