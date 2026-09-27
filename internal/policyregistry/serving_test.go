package policyregistry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/subscriptions/entitlement"
)

const profileKeyOne = "10000000-0000-4000-8000-000000000001"
const profileKeyTwo = "10000000-0000-4000-8000-000000000002"

var servingEpoch = time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

func artifactRef(label string) policyregistry.ObjectRef {
	return policyregistry.ObjectRef{URI: testRegistryRoot + "/fixtures/" + label, SHA256: policyregistry.Digest([]byte(label)), Generation: 1}
}

func servingPayload(t *testing.T, manifest policyregistry.ServingManifest) []byte {
	t.Helper()
	payload, err := policyregistry.CanonicalBytes(manifest)
	require.NoError(t, err)
	return payload
}

func servingRef(t *testing.T, kind policyregistry.ServingKind, manifest policyregistry.ServingManifest) policyregistry.ObjectRef {
	t.Helper()
	payload := servingPayload(t, manifest)
	_, err := policyregistry.DecodeServingManifest(payload, testRegistryRoot, kind)
	require.NoError(t, err)
	return servingStoredRef(t, kind, payload)
}

func servingStoredRef(t *testing.T, kind policyregistry.ServingKind, payload []byte) policyregistry.ObjectRef {
	t.Helper()
	digest := policyregistry.Digest(payload)
	return policyregistry.ObjectRef{URI: testRegistryRoot + "/router_serving/v1/" + string(kind) + "/sha256/" + digest + ".json", SHA256: digest, Generation: 1}
}

// driftedPayload re-encodes canonical manifest bytes the way the drifted registry objects
// were written: a non-Go producer emitted semantically identical JSON with sorted object
// keys instead of Go struct order, plus a trailing newline.
func driftedPayload(t *testing.T, canonical []byte) []byte {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(canonical, &decoded))
	payload, err := json.Marshal(decoded)
	require.NoError(t, err)
	return append(payload, '\n')
}

func namespaceRef(kind policyregistry.ServingKind, label string) policyregistry.ObjectRef {
	digest := policyregistry.Digest([]byte(label))
	return policyregistry.ObjectRef{URI: testRegistryRoot + "/router_serving/v1/" + string(kind) + "/sha256/" + digest + ".json", SHA256: digest, Generation: 1}
}

func fixtureSet(label string) policyregistry.SelectionSet {
	return policyregistry.SelectionSet{SchemaVersion: policyregistry.ServingSelectionSetV1, Target: policyregistry.TargetStable, Default: policyregistry.ServingSelection{Release: namespaceRef(policyregistry.ServingReleases, label), Binding: namespaceRef(policyregistry.ServingBindings, label)}, Profiles: map[string]policyregistry.ServingSelection{}}
}

// fixtureSetV2 is a self-contained v2 selection set whose candidate is referenced but not stored;
// it exercises publish/read plumbing, not lane resolution.
func fixtureSetV2(label string) policyregistry.SelectionSetV2 {
	image := "sha256:" + strings.Repeat("1", 64)
	binding := policyregistry.LaneBinding{Project: "test-project", Region: "test-region",
		Router:      policyregistry.RevisionBinding{Name: "worker-0001", URL: "https://worker-0001.example", Audience: "https://worker.example", ImageDigest: image, Configuration: artifactRef("worker-config")},
		Classifier:  policyregistry.RevisionBinding{Name: "classifier-0001", URL: "https://classifier-0001.example", Audience: "https://classifier.example", ImageDigest: image, Configuration: artifactRef("classifier-config")},
		Attestation: artifactRef("binding-attestation")}
	return policyregistry.SelectionSetV2{SchemaVersion: policyregistry.ServingSelectionSetV2, Target: policyregistry.TargetStable, Default: policyregistry.ServingLane{Candidate: artifactStoredRef([]byte(label)), LaneBinding: binding}, Profiles: map[string]policyregistry.ServingLane{}}
}

// fixtureProposal proposes the fixture set as a new activation must: the activated selection set
// is addressed in the artifacts/ layout the floor requires, while the bound incumbent keeps
// whatever layout the recorded history used. The proposed set is not stored; transition tests
// never read it.
func fixtureProposal(t *testing.T, snapshot policyregistry.ServingStateSnapshot, set policyregistry.SelectionSet, now time.Time) policyregistry.DeploymentProposalV2 {
	t.Helper()
	proposal := policyregistry.DeploymentProposalV2{SchemaVersion: policyregistry.ServingProposalV2, Target: set.Target, SelectionSet: artifactStoredRef(servingPayload(t, set)), SourceCandidate: set.Default.Release, Scope: policyregistry.ChangeFull, Actor: "test-operator", Reason: "release validation", RequestID: uuid.NewString(), CreatedAt: now, Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{}}
	if snapshot.Generation > 0 {
		previous := snapshot.State.Activations[snapshot.State.CurrentActivationID].SelectionSet
		proposal.PreviousSelectionSet = &previous
	}
	return proposal
}

// proposalRef addresses a proposal where every new one is published.
func proposalRef(t *testing.T, proposal policyregistry.DeploymentProposalV2) policyregistry.ObjectRef {
	t.Helper()
	return artifactStoredRef(servingPayload(t, proposal))
}

func activateFixture(t *testing.T, snapshot policyregistry.ServingStateSnapshot, set policyregistry.SelectionSet, now time.Time, withdraw ...string) (policyregistry.ServingStateSnapshot, policyregistry.DeploymentProposalV2) {
	t.Helper()
	proposal := fixtureProposal(t, snapshot, set, now)
	proposal.WithdrawActivations = withdraw
	activated, err := policyregistry.NextServingActivation(snapshot, servingPayload(t, proposal), proposalRef(t, proposal), testRegistryRoot, "test-workflow", now)
	require.NoError(t, err)
	activated.Snapshot.Generation++
	return activated.Snapshot, proposal
}

// storedProposal folds the fixture set into the artifacts/ layout, publishes the v2 objects a new
// activation must name, and proposes them for the given incumbent snapshot.
func storedProposal(t *testing.T, store *servingMemoryStore, snapshot policyregistry.ServingStateSnapshot, set policyregistry.SelectionSet, now time.Time) policyregistry.DeploymentProposalV2 {
	t.Helper()
	proposal := fixtureProposal(t, snapshot, set, now)
	_, setRef := foldSelectionSet(t, store, set)
	proposal.SelectionSet = setRef
	proposal.SourceCandidate = foldCandidate(t, store, set.Default.Release)
	return proposal
}

// variantSet publishes a successor set that differs from its predecessor only in the shared Router
// revision, so consecutive activations bind distinct selection sets.
func variantSet(t *testing.T, store *servingMemoryStore, set policyregistry.SelectionSet, name string) policyregistry.SelectionSet {
	t.Helper()
	binding := *store.object(t, policyregistry.ServingBindings, set.Default.Binding).(*policyregistry.DeploymentBinding)
	router := binding.Router
	router.Name = name
	router.URL = "https://" + name + ".example"
	next := set
	next.Profiles = make(map[string]policyregistry.ServingSelection, len(set.Profiles))
	next.Default = publishSelection(t, store, set.Default, *store.object(t, policyregistry.ServingReleases, set.Default.Release).(*policyregistry.ServingRelease), router, binding.Classifier, set.Default.Profile)
	for key, lane := range set.Profiles {
		laneBinding := *store.object(t, policyregistry.ServingBindings, lane.Binding).(*policyregistry.DeploymentBinding)
		next.Profiles[key] = publishSelection(t, store, lane, *store.object(t, policyregistry.ServingReleases, lane.Release).(*policyregistry.ServingRelease), router, laneBinding.Classifier, lane.Profile)
	}
	return next
}

// v1Proposal builds the historical v1 proposal shape, which validation still accepts for
// pre-floor history even though new activations must name an artifacts/ selection set.
func v1Proposal(t *testing.T, snapshot policyregistry.ServingStateSnapshot, set policyregistry.SelectionSet, now time.Time) policyregistry.DeploymentProposal {
	t.Helper()
	proposal := policyregistry.DeploymentProposal{SchemaVersion: policyregistry.ServingProposalV1, Target: set.Target, ExpectedGeneration: snapshot.Generation, SelectionSet: servingRef(t, policyregistry.ServingSelectionSets, set), SourceRelease: set.Default.Release, Scope: policyregistry.ChangeFull, Actor: "test-operator", Reason: "release validation", RequestID: uuid.NewString(), CreatedAt: now, Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{}}
	if snapshot.Generation > 0 {
		previous := snapshot.State.Activations[snapshot.State.CurrentActivationID].SelectionSet
		proposal.PreviousSelectionSet = &previous
	}
	return proposal
}

// storedActivateFixture records an activation whose objects the controller can read back, which
// history validation and admission both need.
func storedActivateFixture(t *testing.T, store *servingMemoryStore, snapshot policyregistry.ServingStateSnapshot, set policyregistry.SelectionSet, now time.Time) (policyregistry.ServingStateSnapshot, policyregistry.DeploymentProposalV2) {
	t.Helper()
	proposal := storedProposal(t, store, snapshot, set, now)
	activated, err := policyregistry.NextServingActivation(snapshot, servingPayload(t, proposal), store.publishArtifact(t, policyregistry.ServingProposal, proposal), testRegistryRoot, "test-workflow", now)
	require.NoError(t, err)
	activated.Snapshot.Generation++
	return activated.Snapshot, proposal
}

// storedViews projects stored v2 selection sets for admission, keyed as activations declare them.
func storedViews(t *testing.T, store *servingMemoryStore, refs ...policyregistry.ObjectRef) map[string]policyregistry.SelectionSetView {
	t.Helper()
	views := make(map[string]policyregistry.SelectionSetView, len(refs))
	for _, ref := range refs {
		set := store.object(t, policyregistry.ServingSelectionSet, ref).(*policyregistry.SelectionSetV2)
		views[ref.SHA256] = set.View(ref)
	}
	return views
}

func TestServingContractsRejectMutableTargetsAndReferences(t *testing.T) {
	for _, target := range []policyregistry.ServingTarget{"beta", "prod/beta", "stable", "staging/weave-internal", "../stable"} {
		_, err := target.Environment()
		require.Error(t, err)
	}
	for target, environment := range map[policyregistry.ServingTarget]policyregistry.Environment{policyregistry.TargetStaging: policyregistry.EnvironmentStaging, policyregistry.TargetStable: policyregistry.EnvironmentProd, policyregistry.TargetInternal: policyregistry.EnvironmentProd} {
		actual, err := target.Environment()
		require.NoError(t, err)
		assert.Equal(t, environment, actual)
	}
	ref := namespaceRef(policyregistry.ServingReleases, "release")
	require.NoError(t, policyregistry.ValidateServingRef(ref, testRegistryRoot, policyregistry.ServingReleases))
	assert.Error(t, policyregistry.ValidateServingRef(ref, testRegistryRoot, policyregistry.ServingBindings))
	ref.Generation = 0
	assert.Error(t, policyregistry.ValidateServingRef(ref, testRegistryRoot, policyregistry.ServingReleases))
}

func TestServingDecoderRejectsUnknownFieldsSchemaAndTrailingValues(t *testing.T) {
	set := fixtureSet("one")
	payload, err := policyregistry.CanonicalBytes(set)
	require.NoError(t, err)
	for _, invalid := range [][]byte{[]byte(strings.Replace(string(payload), `"schema_version":`, `"unknown":true,"schema_version":`, 1)), []byte(strings.Replace(string(payload), string(policyregistry.ServingSelectionSetV1), "future_v2", 1)), append(append([]byte(nil), payload...), []byte(`{}`)...)} {
		_, err := policyregistry.DecodeServingManifest(invalid, testRegistryRoot, policyregistry.ServingSelectionSets)
		require.Error(t, err)
	}
	set.Profiles = nil
	assert.Error(t, set.Validate(testRegistryRoot))
}

func TestServingDecoderAcceptsAnyValidEncodingButNotSemanticDrift(t *testing.T) {
	set := fixtureSet("drifted")
	canonical := servingPayload(t, set)
	drifted := driftedPayload(t, canonical)
	require.NotEqual(t, canonical, drifted)

	for _, payload := range [][]byte{canonical, drifted, append([]byte("  \n"), canonical...)} {
		manifest, err := policyregistry.DecodeServingManifest(payload, testRegistryRoot, policyregistry.ServingSelectionSets)
		require.NoError(t, err)
		require.Equal(t, &set, manifest)
	}

	invalid := bytes.Replace(drifted, []byte(string(policyregistry.ServingSelectionSetV1)), []byte("future_v2"), 1)
	_, err := policyregistry.DecodeServingManifest(invalid, testRegistryRoot, policyregistry.ServingSelectionSets)
	require.Error(t, err, "encoding freedom does not extend to schema")

	invalid = bytes.Replace(drifted, []byte(`"schema_version":`), []byte(`"unknown":true,"schema_version":`), 1)
	_, err = policyregistry.DecodeServingManifest(invalid, testRegistryRoot, policyregistry.ServingSelectionSets)
	require.Error(t, err)
}

func TestActivationBindsStoredProposalPayloadDigest(t *testing.T) {
	set := fixtureSet("one")
	proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	drifted := driftedPayload(t, servingPayload(t, proposal))
	ref := artifactStoredRef(drifted)
	transition, err := policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, drifted, ref, testRegistryRoot, "workflow", servingEpoch)
	require.NoError(t, err)
	require.Equal(t, proposal.RequestID, transition.Activation.RequestID)
	require.Equal(t, ref, transition.Activation.Proposal)

	mismatched := artifactStoredRef(servingPayload(t, proposal))
	_, err = policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, drifted, mismatched, testRegistryRoot, "workflow", servingEpoch)
	require.ErrorContains(t, err, "digest", "replay is keyed on the recorded proposal ref, so its digest must match the activated bytes")
}

// The layout floor is decided by the proposed selection set's layout alone: identical proposals
// differing only in where the set is stored are rejected and accepted respectively.
func TestNextServingActivationFloorsSelectionSetsOutsideTheArtifactsLayout(t *testing.T) {
	set := fixtureSet("one")
	proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	proposal.SelectionSet = servingStoredRef(t, policyregistry.ServingSelectionSets, servingPayload(t, set))
	_, err := policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, servingPayload(t, proposal), proposalRef(t, proposal), testRegistryRoot, "workflow", servingEpoch)
	require.ErrorIs(t, err, policyregistry.ErrSelectionSetLayoutFloor)
	require.ErrorContains(t, err, proposal.SelectionSet.SHA256)

	proposal.SelectionSet = artifactStoredRef(servingPayload(t, set))
	transition, err := policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, servingPayload(t, proposal), proposalRef(t, proposal), testRegistryRoot, "workflow", servingEpoch)
	require.NoError(t, err)
	require.Equal(t, proposal.SelectionSet, transition.Activation.SelectionSet)
}

func TestServingStoreReadsDriftedObjectsThroughControllerPaths(t *testing.T) {
	ctx := context.Background()
	store, _, initialSet := controllerFixture(t)
	foldedSet, _ := foldSelectionSet(t, store, initialSet)
	driftedSet := driftedPayload(t, servingPayload(t, foldedSet))
	setRef := artifactStoredRef(driftedSet)
	store.putRaw(setRef, driftedSet)

	proposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, initialSet, servingEpoch)
	proposal.SelectionSet = setRef
	driftedProposal := driftedPayload(t, servingPayload(t, proposal))
	driftedRef := artifactStoredRef(driftedProposal)
	store.putRaw(driftedRef, driftedProposal)

	controller := permissiveController(t, store)
	preparation, err := controller.Prepare(ctx, driftedRef)
	require.NoError(t, err)
	require.True(t, preparation.Prepared)
}

func TestControllerVerifiesOnlyTheProposalDigestOnRead(t *testing.T) {
	ctx := context.Background()
	store, _, set := controllerFixture(t)
	controller := permissiveController(t, store)

	proposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	payload := servingPayload(t, proposal)
	mislabeled := artifactStoredRef(append(append([]byte(nil), payload...), '\n'))
	store.putRaw(mislabeled, payload)
	_, err := controller.Prepare(ctx, mislabeled)
	require.ErrorContains(t, err, "digest")
	_, err = controller.Activate(ctx, mislabeled, "workflow")
	require.ErrorContains(t, err, "digest")
	require.Empty(t, store.states)

	foldedSet, _ := foldSelectionSet(t, store, set)
	relabeledSet := artifactStoredRef([]byte("relabeled"))
	store.putRaw(relabeledSet, servingPayload(t, foldedSet))
	proposal.SelectionSet = relabeledSet
	ref := store.publishArtifact(t, policyregistry.ServingProposal, proposal)
	preparation, err := controller.Prepare(ctx, ref)
	require.NoError(t, err, "traversal reads trust the generation-pinned reference")
	require.True(t, preparation.Prepared)
}

func TestActivationIncarnationsNeverResetEarlierRetirement(t *testing.T) {
	firstSet, secondSet := fixtureSet("one"), fixtureSet("two")
	first, firstProposal := activateFixture(t, policyregistry.ServingStateSnapshot{}, firstSet, servingEpoch)
	second, _ := activateFixture(t, first, secondSet, servingEpoch.Add(time.Hour))
	third, _ := activateFixture(t, second, firstSet, servingEpoch.Add(2*time.Hour))
	firstID, thirdID := first.State.CurrentActivationID, third.State.CurrentActivationID
	assert.NotEqual(t, firstID, thirdID)
	assert.Equal(t, third.State.Activations[firstID].SelectionSet, third.State.Activations[thirdID].SelectionSet)
	assert.Equal(t, servingEpoch.Add(time.Hour), *third.State.Activations[firstID].SupersededAt)
	assert.Nil(t, first.State.Activations[firstID].SupersededAt, "pure transition must not mutate the input snapshot")
	assert.Equal(t, int64(3), third.State.Sequence)
	retry, err := policyregistry.NextServingActivation(third, servingPayload(t, firstProposal), proposalRef(t, firstProposal), testRegistryRoot, "retry-workflow", servingEpoch.Add(3*time.Hour))
	require.NoError(t, err)
	assert.True(t, retry.Replayed)
	assert.Equal(t, policyregistry.ActivationSuperseded, retry.Outcome)
	assert.Equal(t, firstID, retry.Activation.ID)
	assert.Equal(t, thirdID, retry.Snapshot.State.CurrentActivationID)
}

func TestActivationRejectsStalePreview(t *testing.T) {
	set := fixtureSet("one")
	first, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	stale := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	_, err := policyregistry.NextServingActivation(first, servingPayload(t, stale), proposalRef(t, stale), testRegistryRoot, "workflow", servingEpoch)
	require.ErrorIs(t, err, policyregistry.ErrConflict)
}

func TestActivationReplayIsKeyedOnProposalRefNotRequestID(t *testing.T) {
	set := fixtureSet("one")
	first, firstProposal := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	firstID := first.State.CurrentActivationID

	secondProposal := fixtureProposal(t, first, fixtureSet("two"), servingEpoch.Add(time.Hour))
	secondProposal.RequestID = firstProposal.RequestID
	secondRef := proposalRef(t, secondProposal)
	second, err := policyregistry.NextServingActivation(first, servingPayload(t, secondProposal), secondRef, testRegistryRoot, "workflow", servingEpoch.Add(time.Hour))
	require.NoError(t, err, "a different proposal sharing a request ID is a second activation, not a conflict")
	require.False(t, second.Replayed)
	require.NotEqual(t, firstID, second.Activation.ID)
	require.Equal(t, int64(2), second.Activation.Sequence)
	require.Equal(t, firstProposal.RequestID, second.Activation.RequestID)
	require.Equal(t, firstProposal.RequestID, second.Snapshot.State.Activations[firstID].RequestID)
	require.NoError(t, second.Snapshot.State.Validate(testRegistryRoot, set.Target), "shared request IDs are audit metadata, not a state invariant")

	second.Snapshot.Generation++
	replay, err := policyregistry.NextServingActivation(second.Snapshot, servingPayload(t, secondProposal), secondRef, testRegistryRoot, "retry-workflow", servingEpoch.Add(2*time.Hour))
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, second.Activation.ID, replay.Activation.ID)
	require.Equal(t, policyregistry.ActivationCurrent, replay.Outcome)
	require.Equal(t, second.Snapshot, replay.Snapshot, "replay must not build a new transition")
}

func TestServingRequestIDIsAnyNonEmptyBoundedString(t *testing.T) {
	set := fixtureSet("one")
	for _, requestID := range []string{"run-123456789:lane-0", "a", strings.Repeat("x", policyregistry.MaxServingRequestIDLength)} {
		proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
		proposal.RequestID = requestID
		require.NoError(t, proposal.Validate(testRegistryRoot))
		activated, err := policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, servingPayload(t, proposal), proposalRef(t, proposal), testRegistryRoot, "workflow", servingEpoch)
		require.NoError(t, err)
		require.Equal(t, requestID, activated.Activation.RequestID)
		require.NoError(t, activated.Snapshot.State.Validate(testRegistryRoot, set.Target))
	}
	for _, requestID := range []string{"", "   ", strings.Repeat("x", policyregistry.MaxServingRequestIDLength+1)} {
		proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
		proposal.RequestID = requestID
		require.ErrorContains(t, proposal.Validate(testRegistryRoot), "request ID")
		activated, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
		current := activated.State.Activations[activated.State.CurrentActivationID]
		current.RequestID = requestID
		activated.State.Activations[current.ID] = current
		require.ErrorContains(t, activated.State.Validate(testRegistryRoot, set.Target), "request identity")
	}
}

func TestSessionReleaseIdleAndSupersessionBoundaries(t *testing.T) {
	firstSet, secondSet := fixtureSet("one"), fixtureSet("two")
	first, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, firstSet, servingEpoch)
	second, _ := activateFixture(t, first, secondSet, servingEpoch.Add(time.Hour))
	sets := map[string]policyregistry.SelectionSetView{servingRef(t, policyregistry.ServingSelectionSets, firstSet).SHA256: firstSet.View(), servingRef(t, policyregistry.ServingSelectionSets, secondSet).SHA256: secondSet.View()}
	projection := policyregistry.AdmissionProjection{Target: policyregistry.TargetStable}
	initial, err := policyregistry.SelectSessionRelease(nil, projection, first, sets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)
	for _, tc := range []struct {
		name          string
		now           time.Time
		lastAdmission time.Time
		retained      bool
	}{
		{"before_idle", servingEpoch.Add(24*time.Hour - time.Nanosecond), servingEpoch, true},
		{"at_idle", servingEpoch.Add(24 * time.Hour), servingEpoch, false},
		{"before_retirement", servingEpoch.Add(time.Hour + 7*24*time.Hour - time.Nanosecond), servingEpoch.Add(7 * 24 * time.Hour), true},
		{"at_retirement", servingEpoch.Add(time.Hour + 7*24*time.Hour), servingEpoch.Add(7 * 24 * time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := initial
			previous.LastAdmittedAt = tc.lastAdmission
			admitted, err := policyregistry.SelectSessionRelease(&previous, projection, second, sets, testRegistryRoot, tc.now)
			require.NoError(t, err)
			if tc.retained {
				assert.Equal(t, first.State.CurrentActivationID, admitted.ActivationID)
				assert.Equal(t, int64(1), admitted.BindingGeneration)
			} else {
				assert.Equal(t, second.State.CurrentActivationID, admitted.ActivationID)
				assert.Equal(t, int64(2), admitted.BindingGeneration)
			}
			assert.Equal(t, tc.now, admitted.LastAdmittedAt)
			assert.Equal(t, servingEpoch, admitted.CreatedAt)
		})
	}
}

func TestEmergencyWithdrawalRebindsNextAdmissionButDoesNotMutateInFlight(t *testing.T) {
	set := fixtureSet("one")
	first, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	sets := map[string]policyregistry.SelectionSetView{servingRef(t, policyregistry.ServingSelectionSets, set).SHA256: set.View()}
	projection := policyregistry.AdmissionProjection{Target: policyregistry.TargetStable}
	inFlight, err := policyregistry.SelectSessionRelease(nil, projection, first, sets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)
	rollback, _ := activateFixture(t, first, set, servingEpoch.Add(time.Minute), first.State.CurrentActivationID)
	next, err := policyregistry.SelectSessionRelease(&inFlight, projection, rollback, sets, testRegistryRoot, servingEpoch.Add(2*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, first.State.CurrentActivationID, inFlight.ActivationID)
	assert.Equal(t, rollback.State.CurrentActivationID, next.ActivationID)
	assert.Equal(t, inFlight.Selection, next.Selection)
	assert.Equal(t, int64(2), next.BindingGeneration)
	assert.Equal(t, rollback.State.CurrentActivationID, rollback.State.Activations[inFlight.ActivationID].ReplacementID)
}

func TestEmergencyWithdrawalPreservesSubscriberPlanAndProfile(t *testing.T) {
	for _, plan := range []entitlement.Plan{entitlement.PlanMax, entitlement.PlanBoost} {
		t.Run(string(plan), func(t *testing.T) {
			profile, ok := entitlement.ServingProfileFor(plan)
			require.True(t, ok)
			previousSet, replacementSet := fixtureSet("old-worker"), fixtureSet("ready-worker")
			profileRef := namespaceRef(policyregistry.ServingProfiles, "unchanged-profile")
			previousSet.Profiles[profile.Key] = policyregistry.ServingSelection{Release: namespaceRef(policyregistry.ServingReleases, "old-profile-worker"), Binding: namespaceRef(policyregistry.ServingBindings, "old-profile-worker"), Profile: &profileRef}
			replacementSet.Profiles[profile.Key] = policyregistry.ServingSelection{Release: namespaceRef(policyregistry.ServingReleases, "ready-profile-worker"), Binding: namespaceRef(policyregistry.ServingBindings, "ready-profile-worker"), Profile: &profileRef}
			first, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, previousSet, servingEpoch)
			sets := map[string]policyregistry.SelectionSetView{
				servingRef(t, policyregistry.ServingSelectionSets, previousSet).SHA256:    previousSet.View(),
				servingRef(t, policyregistry.ServingSelectionSets, replacementSet).SHA256: replacementSet.View(),
			}
			projection := policyregistry.AdmissionProjection{Target: policyregistry.TargetStable, ProfileKey: profile.Key, ProfileName: profile.Name, Plan: plan, EntitlementVersion: 3, EnrollmentGeneration: 2, AssignmentGeneration: 4}
			inFlight, err := policyregistry.SelectSessionRelease(nil, projection, first, sets, testRegistryRoot, servingEpoch)
			require.NoError(t, err)
			replacement, _ := activateFixture(t, first, replacementSet, servingEpoch.Add(time.Minute), first.State.CurrentActivationID)
			next, err := policyregistry.SelectSessionRelease(&inFlight, projection, replacement, sets, testRegistryRoot, servingEpoch.Add(2*time.Minute))
			require.NoError(t, err)
			assert.Equal(t, first.State.CurrentActivationID, inFlight.ActivationID)
			assert.Equal(t, previousSet.Profiles[profile.Key], inFlight.Selection)
			assert.Equal(t, replacement.State.CurrentActivationID, next.ActivationID)
			assert.Equal(t, replacementSet.Profiles[profile.Key], next.Selection)
			assert.Equal(t, profile.Key, next.ProfileKey)
			assert.Equal(t, profile.Name, next.ProfileName)
			assert.Equal(t, plan, next.Plan)
			assert.Equal(t, int64(3), next.EntitlementVersion)
			assert.Equal(t, int64(2), next.EnrollmentGeneration)
			assert.Equal(t, int64(4), next.AssignmentGeneration)
			assert.Equal(t, int64(2), next.BindingGeneration)
			assert.Equal(t, servingEpoch, next.CreatedAt)
		})
	}
}

func TestProfileAssignmentsDoNotFallBackAndGenerationChangesRebind(t *testing.T) {
	set := fixtureSet("one")
	profile := namespaceRef(policyregistry.ServingProfiles, "profile")
	set.Profiles[profileKeyOne] = policyregistry.ServingSelection{Release: namespaceRef(policyregistry.ServingReleases, "custom"), Binding: namespaceRef(policyregistry.ServingBindings, "custom"), Profile: &profile}
	first, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	sets := map[string]policyregistry.SelectionSetView{servingRef(t, policyregistry.ServingSelectionSets, set).SHA256: set.View()}
	projection := policyregistry.AdmissionProjection{Target: policyregistry.TargetStable, ProfileKey: profileKeyOne, AssignmentGeneration: 1}
	initial, err := policyregistry.SelectSessionRelease(nil, projection, first, sets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)
	assert.Equal(t, set.Profiles[profileKeyOne], initial.Selection)
	projection.AssignmentGeneration++
	next, err := policyregistry.SelectSessionRelease(&initial, projection, first, sets, testRegistryRoot, servingEpoch.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(2), next.BindingGeneration)
	assert.Equal(t, profileKeyOne, next.ProfileKey)
	projection.ProfileKey = profileKeyTwo
	_, err = policyregistry.SelectSessionRelease(&initial, projection, first, sets, testRegistryRoot, servingEpoch.Add(time.Minute))
	require.ErrorContains(t, err, "default fallback is forbidden")
	initial.ActivationID = uuid.NewString()
	_, err = policyregistry.SelectSessionRelease(&initial, projection, first, sets, testRegistryRoot, servingEpoch.Add(time.Minute))
	require.ErrorContains(t, err, "unknown activation")
}

func TestSubscriberPlanProfileMustMatchServerOwnedMapping(t *testing.T) {
	profile, ok := entitlement.ServingProfileFor(entitlement.PlanMax)
	require.True(t, ok)
	set := fixtureSet("one")
	profileRef := namespaceRef(policyregistry.ServingProfiles, "max-profile")
	set.Profiles[profile.Key] = policyregistry.ServingSelection{Release: namespaceRef(policyregistry.ServingReleases, "max"), Binding: namespaceRef(policyregistry.ServingBindings, "max"), Profile: &profileRef}
	snapshot, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	sets := map[string]policyregistry.SelectionSetView{servingRef(t, policyregistry.ServingSelectionSets, set).SHA256: set.View()}
	projection := policyregistry.AdmissionProjection{
		Target:               policyregistry.TargetStable,
		ProfileKey:           profile.Key,
		ProfileName:          profile.Name,
		Plan:                 entitlement.PlanMax,
		EntitlementVersion:   3,
		AssignmentGeneration: 3,
	}

	admitted, err := policyregistry.SelectSessionRelease(nil, projection, snapshot, sets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)
	assert.Equal(t, profile.Name, admitted.ProfileName)
	assert.Equal(t, entitlement.PlanMax, admitted.Plan)
	assert.Equal(t, int64(3), admitted.EntitlementVersion)

	projection.ProfileName = "customer-profile"
	_, err = policyregistry.SelectSessionRelease(nil, projection, snapshot, sets, testRegistryRoot, servingEpoch)
	require.ErrorContains(t, err, "subscriber plan profile projection is invalid")
	projection.ProfileName = profile.Name
	projection.ProfileKey = profileKeyOne
	_, err = policyregistry.SelectSessionRelease(nil, projection, snapshot, sets, testRegistryRoot, servingEpoch)
	require.ErrorContains(t, err, "subscriber plan profile projection is invalid")
}

type servingMemoryStore struct {
	mu       sync.Mutex
	objects  map[policyregistry.ObjectRef][]byte
	policies map[policyregistry.ObjectRef]*rosterdata.Roster
	states   map[policyregistry.ServingTarget]policyregistry.ServingStateSnapshot
	// legacyStates models runtime_state/... objects: read only when no state/ object exists, never written.
	legacyStates map[policyregistry.ServingTarget]policyregistry.ServingStateSnapshot
	readErr      error
	casErr       error
	casCalls     int
	artifacts    map[policyregistry.ObjectRef][]byte
}

func newServingMemoryStore() *servingMemoryStore {
	artifacts := make(map[policyregistry.ObjectRef][]byte)
	for _, label := range []string{"build-attestation", "binding-attestation", "evidence"} {
		artifacts[artifactRef(label)] = []byte(label)
	}
	return &servingMemoryStore{objects: make(map[policyregistry.ObjectRef][]byte), policies: make(map[policyregistry.ObjectRef]*rosterdata.Roster), states: make(map[policyregistry.ServingTarget]policyregistry.ServingStateSnapshot), legacyStates: make(map[policyregistry.ServingTarget]policyregistry.ServingStateSnapshot), artifacts: artifacts}
}

func (s *servingMemoryStore) VerifyServingArtifact(_ context.Context, ref policyregistry.ObjectRef) error {
	payload, exists := s.artifacts[ref]
	if !exists {
		return policyregistry.ErrNotFound
	}
	if policyregistry.Digest(payload) != ref.SHA256 {
		return errors.New("artifact digest mismatch")
	}
	return nil
}

func (s *servingMemoryStore) RootURI() string { return testRegistryRoot }
func (s *servingMemoryStore) ReadServingObject(_ context.Context, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	payload, exists := s.objects[ref]
	if !exists {
		return nil, nil, policyregistry.ErrNotFound
	}
	manifest, err := policyregistry.DecodeServingObject(payload, testRegistryRoot, kind, ref)
	if err != nil {
		return nil, nil, err
	}
	return manifest, payload, nil
}
func (s *servingMemoryStore) ReadServingPolicy(_ context.Context, ref policyregistry.ObjectRef) (*rosterdata.Roster, error) {
	policy, exists := s.policies[ref]
	if !exists {
		return nil, policyregistry.ErrNotFound
	}
	return policy, nil
}
func (s *servingMemoryStore) ReadServingState(_ context.Context, target policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return policyregistry.ServingStateSnapshot{}, s.readErr
	}
	if snapshot, exists := s.states[target]; exists {
		return snapshot, nil
	}
	if snapshot, exists := s.legacyStates[target]; exists {
		snapshot.LegacyPath = true
		return snapshot, nil
	}
	return policyregistry.ServingStateSnapshot{}, policyregistry.ErrNotFound
}
func (s *servingMemoryStore) CompareAndSwapServingState(_ context.Context, next policyregistry.ServingControlState, expected int64) (policyregistry.ServingStateSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.casCalls++
	if s.casErr != nil {
		return policyregistry.ServingStateSnapshot{}, s.casErr
	}
	if s.states[next.Target].Generation != expected {
		return policyregistry.ServingStateSnapshot{}, policyregistry.ErrConflict
	}
	snapshot := policyregistry.ServingStateSnapshot{State: next, Generation: expected + 1}
	s.states[next.Target] = snapshot
	return snapshot, nil
}
func (s *servingMemoryStore) publish(t *testing.T, kind policyregistry.ServingKind, manifest policyregistry.ServingManifest) policyregistry.ObjectRef {
	t.Helper()
	ref := servingRef(t, kind, manifest)
	payload, err := policyregistry.CanonicalBytes(manifest)
	require.NoError(t, err)
	s.putRaw(ref, payload)
	return ref
}

// putRaw stores arbitrary bytes under a reference without publish-time validation.
func (s *servingMemoryStore) putRaw(ref policyregistry.ObjectRef, payload []byte) {
	s.objects[ref] = payload
}

// object decodes a stored payload for test assertions without going through ServingStore reads.
func (s *servingMemoryStore) object(t *testing.T, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) policyregistry.ServingManifest {
	t.Helper()
	manifest, _, err := s.ReadServingObject(context.Background(), kind, ref)
	require.NoError(t, err)
	return manifest
}

type preparedValidator func(context.Context, policyregistry.PreparedSelection) error

func (f preparedValidator) ValidatePreparedSelection(ctx context.Context, selection policyregistry.PreparedSelection) error {
	return f(ctx, selection)
}

func controllerFixture(t *testing.T) (*servingMemoryStore, *policyregistry.ServingController, policyregistry.SelectionSet) {
	t.Helper()
	store := newServingMemoryStore()
	legacy := validLoader(t)
	policyBytes, err := rosterdata.CanonicalBytes(legacy.policy)
	require.NoError(t, err)
	policyDigest := policyregistry.Digest(policyBytes)
	policyRef := policyregistry.ObjectRef{URI: testRegistryRoot + "/router_policy/v1/policies/sha256/" + policyDigest + ".json", SHA256: policyDigest, Generation: 1}
	store.policies[policyRef] = legacy.policy
	packageRef := artifactRef("classifier-package")
	identity := legacy.release.Classifier
	identity.PackageSHA256 = packageRef.SHA256
	bundle := policyregistry.ClassifierBundle{SchemaVersion: policyregistry.ServingClassifierV1, Identity: identity, Package: packageRef, Configuration: artifactRef("classifier-config"), AuxiliaryModels: map[string]policyregistry.ObjectRef{"escalation": artifactRef("escalation")}}
	bundleRef := store.publish(t, policyregistry.ServingClassifiers, bundle)
	release := policyregistry.ServingRelease{SchemaVersion: policyregistry.ServingReleaseV1, RouterImageDigest: "sha256:" + strings.Repeat("1", 64), Policy: policyregistry.PolicyObject{URI: policyRef.URI, SHA256: policyRef.SHA256, Generation: policyRef.Generation, SchemaVersion: legacy.policy.SchemaVersion}, Classifier: bundleRef, Requirements: policyregistry.ServingRequirements{RuntimeContract: policyregistry.ManagedRuntimeContractV1, PolicySchema: legacy.policy.SchemaVersion, ClassifierWireSchema: identity.WireSchema, TaxonomySHA256: identity.TaxonomySHA256}, Provenance: policyregistry.ServingProvenance{RouterRevision: strings.Repeat("2", 40), WeaveRevision: strings.Repeat("3", 40), BuildAttestation: artifactRef("build-attestation")}}
	releaseRef := store.publish(t, policyregistry.ServingReleases, release)
	binding := policyregistry.DeploymentBinding{SchemaVersion: policyregistry.ServingBindingV1, Target: policyregistry.TargetStable, Project: "test-project", Region: "test-region", Release: releaseRef, Router: policyregistry.RevisionBinding{Name: "worker-0001", URL: "https://worker-0001.example", Audience: "https://worker.example", ImageDigest: release.RouterImageDigest, Configuration: artifactRef("worker-config")}, Classifier: policyregistry.RevisionBinding{Name: "classifier-0001", URL: "https://classifier-0001.example", Audience: "https://classifier.example", ImageDigest: identity.ImageDigest, Configuration: bundle.Configuration}, ClassifierBundleSHA256: bundleRef.SHA256, Attestation: artifactRef("binding-attestation")}
	bindingRef := store.publish(t, policyregistry.ServingBindings, binding)
	set := policyregistry.SelectionSet{SchemaVersion: policyregistry.ServingSelectionSetV1, Target: policyregistry.TargetStable, Default: policyregistry.ServingSelection{Release: releaseRef, Binding: bindingRef}, Profiles: map[string]policyregistry.ServingSelection{}}
	store.publish(t, policyregistry.ServingSelectionSets, set)
	validator := preparedValidator(func(_ context.Context, selection policyregistry.PreparedSelection) error {
		if selection.Binding.Router.ImageDigest != "sha256:"+strings.Repeat("1", 64) {
			return errors.New("worker attestation mismatch")
		}
		if selection.Candidate.Classifier.AuxiliaryModels["escalation"] != artifactRef("escalation") {
			return errors.New("auxiliary model mismatch")
		}
		return nil
	})
	controller, err := policyregistry.NewServingController(store, validator, func() time.Time { return servingEpoch }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	return store, controller, set
}

func TestServingControllerCASAndIdempotency(t *testing.T) {
	store, controller, set := controllerFixture(t)
	proposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	ref := store.publishArtifact(t, policyregistry.ServingProposal, proposal)
	activated, err := controller.Activate(context.Background(), ref, "workflow")
	require.NoError(t, err)
	assert.Equal(t, int64(1), activated.Snapshot.Generation)
	retry, err := controller.Activate(context.Background(), ref, "workflow-retry")
	require.NoError(t, err)
	assert.True(t, retry.Replayed)
	assert.Equal(t, activated.Activation.ID, retry.Activation.ID)
	assert.Equal(t, "test-operator", retry.Activation.Actor)
	assert.Equal(t, "workflow", retry.Activation.WorkflowActor)
	proposal.RequestID = uuid.NewString()
	staleRef := store.publishArtifact(t, policyregistry.ServingProposal, proposal)
	_, err = controller.Activate(context.Background(), staleRef, "workflow")
	require.ErrorIs(t, err, policyregistry.ErrConflict)
}

func TestServingControllerCommitsTheSingleTransitionBuiltBeforeValidation(t *testing.T) {
	store, _, set := controllerFixture(t)
	clockCalls := 0
	clock := func() time.Time {
		clockCalls++
		return servingEpoch.Add(time.Duration(clockCalls) * time.Minute)
	}
	validations := 0
	var moveGeneration func()
	validator := preparedValidator(func(context.Context, policyregistry.PreparedSelection) error {
		validations++
		if moveGeneration != nil {
			moveGeneration()
		}
		return nil
	})
	controller, err := policyregistry.NewServingController(store, validator, clock, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	proposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	ref := store.publishArtifact(t, policyregistry.ServingProposal, proposal)

	activated, err := controller.Activate(context.Background(), ref, "workflow")
	require.NoError(t, err)
	require.Equal(t, 1, clockCalls, "the transition is computed once, before destination validation")
	require.Equal(t, 1, validations)
	require.Equal(t, 1, store.casCalls)
	require.Equal(t, servingEpoch.Add(time.Minute), activated.Activation.ActivatedAt)
	require.Equal(t, activated.Snapshot, store.states[set.Target])

	// A concurrent writer landing during slow destination validation is caught by the CAS write alone.
	moveGeneration = func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		moved := store.states[set.Target]
		moved.Generation++
		store.states[set.Target] = moved
	}
	next := storedProposal(t, store, activated.Snapshot, set, servingEpoch)
	nextRef := store.publishArtifact(t, policyregistry.ServingProposal, next)
	_, err = controller.Activate(context.Background(), nextRef, "workflow")
	require.ErrorIs(t, err, policyregistry.ErrConflict)
	require.Equal(t, 2, clockCalls)
	require.Equal(t, 2, store.casCalls)
	require.Equal(t, activated.Snapshot.State, store.states[set.Target].State, "a lost CAS race must not commit the stale transition")
}

func TestServingControllerFailsClosedOnRegistryAndCASFailure(t *testing.T) {
	for _, failRead := range []bool{true, false} {
		store, controller, set := controllerFixture(t)
		proposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
		ref := store.publishArtifact(t, policyregistry.ServingProposal, proposal)
		failure := errors.New("storage unavailable")
		if failRead {
			store.readErr = failure
		} else {
			store.casErr = failure
		}
		_, err := controller.Activate(context.Background(), ref, "workflow")
		require.ErrorIs(t, err, failure)
		assert.Empty(t, store.states)
	}
}

func TestServingControllerRejectsUnverifiedProposalEvidence(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{name: "missing evidence"},
		{name: "digest mismatch", payload: []byte("changed evidence")},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, controller, set := controllerFixture(t)
			proposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
			ref := store.publishArtifact(t, policyregistry.ServingProposal, proposal)
			delete(store.artifacts, proposal.Evidence[0])
			if test.payload != nil {
				store.artifacts[proposal.Evidence[0]] = test.payload
			}
			_, err := controller.Prepare(context.Background(), ref)
			require.ErrorContains(t, err, "verify proposal evidence")
			_, err = controller.Activate(context.Background(), ref, "workflow")
			require.ErrorContains(t, err, "verify proposal evidence")
			require.Empty(t, store.states, "invalid evidence must never publish an activation")
		})
	}
}

func TestServingControllerRejectsAttestedCodeMismatch(t *testing.T) {
	store, controller, set := controllerFixture(t)
	release := *store.object(t, policyregistry.ServingReleases, set.Default.Release).(*policyregistry.ServingRelease)
	release.RouterImageDigest = "sha256:" + strings.Repeat("9", 64)
	set.Default.Release = store.publish(t, policyregistry.ServingReleases, release)
	store.publish(t, policyregistry.ServingSelectionSets, set)
	proposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	ref := store.publishArtifact(t, policyregistry.ServingProposal, proposal)
	_, err := controller.Activate(context.Background(), ref, "workflow")
	require.ErrorContains(t, err, "binding differs")
	assert.Empty(t, store.states)
}

func TestClassifierBundleIdentityIncludesAuxiliaryModels(t *testing.T) {
	store, _, set := controllerFixture(t)
	release := store.object(t, policyregistry.ServingReleases, set.Default.Release).(*policyregistry.ServingRelease)
	bundle := *store.object(t, policyregistry.ServingClassifiers, release.Classifier).(*policyregistry.ClassifierBundle)
	bundle.AuxiliaryModels = maps.Clone(bundle.AuxiliaryModels)
	bundle.AuxiliaryModels["escalation"] = artifactRef("different-escalation")
	changed := servingRef(t, policyregistry.ServingClassifiers, bundle)
	assert.NotEqual(t, release.Classifier.SHA256, changed.SHA256)
	bundle.Configuration.Generation = 0
	assert.Error(t, bundle.Validate(testRegistryRoot))
}

func registerProfileFixture(t *testing.T, store *servingMemoryStore, base policyregistry.ServingSelection, key string, policy policyregistry.PolicyObject) policyregistry.ServingSelection {
	t.Helper()
	release := *store.object(t, policyregistry.ServingReleases, base.Release).(*policyregistry.ServingRelease)
	release.Policy = policy
	releaseRef := store.publish(t, policyregistry.ServingReleases, release)
	profileRef := store.publish(t, policyregistry.ServingProfiles, policyregistry.RoutingProfile{SchemaVersion: policyregistry.ServingProfileV1, ProfileKey: key, Policy: policy, Requirements: release.Requirements})
	binding := *store.object(t, policyregistry.ServingBindings, base.Binding).(*policyregistry.DeploymentBinding)
	binding.Release = releaseRef
	bindingRef := store.publish(t, policyregistry.ServingBindings, binding)
	return policyregistry.ServingSelection{Release: releaseRef, Binding: bindingRef, Profile: &profileRef}
}

func TestProfileVersionAdvancePreservesOtherCustomerAndDefault(t *testing.T) {
	store, controller, initialSet := controllerFixture(t)
	base := *store.object(t, policyregistry.ServingReleases, initialSet.Default.Release).(*policyregistry.ServingRelease)
	initialSet.Profiles[profileKeyOne] = registerProfileFixture(t, store, initialSet.Default, profileKeyOne, base.Policy)
	initialSet.Profiles[profileKeyTwo] = registerProfileFixture(t, store, initialSet.Default, profileKeyTwo, base.Policy)
	store.publish(t, policyregistry.ServingSelectionSets, initialSet)
	initialProposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, initialSet, servingEpoch)
	initial, err := controller.Activate(context.Background(), store.publishArtifact(t, policyregistry.ServingProposal, initialProposal), "workflow")
	require.NoError(t, err)
	policyRef := policyregistry.ObjectRef{URI: base.Policy.URI, SHA256: base.Policy.SHA256, Generation: base.Policy.Generation}
	policyBytes, err := rosterdata.CanonicalBytes(store.policies[policyRef])
	require.NoError(t, err)
	changedPolicy, err := rosterdata.ParseValidated(policyBytes)
	require.NoError(t, err)
	changedPolicy.Preferences.PreferredModelBonus = 0.6
	changedBytes, err := rosterdata.CanonicalBytes(changedPolicy)
	require.NoError(t, err)
	changedDigest := policyregistry.Digest(changedBytes)
	changedRef := policyregistry.ObjectRef{URI: testRegistryRoot + "/router_policy/v1/policies/sha256/" + changedDigest + ".json", SHA256: changedDigest, Generation: 1}
	store.policies[changedRef] = changedPolicy
	changedPolicyObject := policyregistry.PolicyObject{URI: changedRef.URI, SHA256: changedRef.SHA256, Generation: changedRef.Generation, SchemaVersion: changedPolicy.SchemaVersion}
	updatedSet := initialSet
	updatedSet.Profiles = maps.Clone(initialSet.Profiles)
	updatedSet.Profiles[profileKeyOne] = registerProfileFixture(t, store, initialSet.Default, profileKeyOne, changedPolicyObject)
	store.publish(t, policyregistry.ServingSelectionSets, updatedSet)
	proposal := storedProposal(t, store, initial.Snapshot, updatedSet, servingEpoch)
	proposal.Scope = policyregistry.ChangeProfile
	proposal.ProfileKey = profileKeyOne
	proposal.SourceCandidate = foldCandidate(t, store, updatedSet.Profiles[profileKeyOne].Release)
	updated, err := controller.Activate(context.Background(), store.publishArtifact(t, policyregistry.ServingProposal, proposal), "workflow")
	require.NoError(t, err)
	assert.Equal(t, initialSet.Default, updatedSet.Default)
	assert.Equal(t, initialSet.Profiles[profileKeyTwo], updatedSet.Profiles[profileKeyTwo])
	assert.NotEqual(t, initialSet.Profiles[profileKeyOne].Release, updatedSet.Profiles[profileKeyOne].Release)
	sets := storedViews(t, store, initial.Activation.SelectionSet, updated.Activation.SelectionSet)
	initialView, updatedView := sets[initial.Activation.SelectionSet.SHA256], sets[updated.Activation.SelectionSet.SHA256]
	projection := policyregistry.AdmissionProjection{Target: policyregistry.TargetStable, ProfileKey: profileKeyOne, AssignmentGeneration: 1}
	previous, err := policyregistry.SelectSessionRelease(nil, projection, initial.Snapshot, sets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)
	retained, err := policyregistry.SelectSessionRelease(&previous, projection, updated.Snapshot, sets, testRegistryRoot, servingEpoch.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, initialView.Profiles[profileKeyOne], retained.Selection)
	newConversation, err := policyregistry.SelectSessionRelease(nil, projection, updated.Snapshot, sets, testRegistryRoot, servingEpoch.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, updatedView.Profiles[profileKeyOne], newConversation.Selection)
	delete(updatedSet.Profiles, profileKeyTwo)
	store.publish(t, policyregistry.ServingSelectionSets, updatedSet)
	invalid := storedProposal(t, store, updated.Snapshot, updatedSet, servingEpoch)
	invalid.Scope, invalid.ProfileKey, invalid.SourceCandidate = policyregistry.ChangeProfile, profileKeyOne, proposal.SourceCandidate
	require.ErrorContains(t, controller.ValidateProposal(context.Background(), invalid), "cannot be removed")
}

func TestConcurrentServingActivationsHaveOneCASWinner(t *testing.T) {
	store, _, set := controllerFixture(t)
	first := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	second := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	refs := []policyregistry.ObjectRef{store.publishArtifact(t, policyregistry.ServingProposal, first), store.publishArtifact(t, policyregistry.ServingProposal, second)}
	ready := make(chan struct{}, 2)
	releaseValidation := make(chan struct{})
	validator := preparedValidator(func(ctx context.Context, _ policyregistry.PreparedSelection) error {
		ready <- struct{}{}
		select {
		case <-releaseValidation:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	controller, err := policyregistry.NewServingController(store, validator, func() time.Time { return servingEpoch }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	outcomes := make(chan error, 2)
	for _, ref := range refs {
		go func() {
			_, err := controller.Activate(ctx, ref, "workflow")
			outcomes <- err
		}()
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("both proposals did not reach validation")
		}
	}
	close(releaseValidation)
	winners, conflicts := 0, 0
	for range 2 {
		err := <-outcomes
		if err == nil {
			winners++
		} else if errors.Is(err, policyregistry.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	assert.Equal(t, 1, winners)
	assert.Equal(t, 1, conflicts)
	assert.Equal(t, int64(1), store.states[policyregistry.TargetStable].State.Sequence)
}
