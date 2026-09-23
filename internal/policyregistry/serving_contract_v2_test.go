package policyregistry_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

// artifactStoredRef addresses published bytes in the flat v2 layout.
func artifactStoredRef(payload []byte) policyregistry.ObjectRef {
	digest := policyregistry.Digest(payload)
	return policyregistry.ObjectRef{URI: testRegistryRoot + "/artifacts/" + digest + ".json", SHA256: digest, Generation: 1}
}

// publishArtifact stores a v2 manifest under artifacts/ after proving it decodes for its family kind.
func (s *servingMemoryStore) publishArtifact(t *testing.T, kind policyregistry.ServingKind, manifest policyregistry.ServingManifest) policyregistry.ObjectRef {
	t.Helper()
	payload := servingPayload(t, manifest)
	_, err := policyregistry.DecodeServingManifest(payload, testRegistryRoot, kind)
	require.NoError(t, err)
	ref := artifactStoredRef(payload)
	s.putRaw(ref, payload)
	return ref
}

// v2Fixture expresses the controller fixture's v1 composition (release + classifier bundle +
// deployment binding, plus one registered profile) as a v2 candidate and selection set.
type v2Fixture struct {
	v1        policyregistry.SelectionSet
	candidate policyregistry.ObjectRef
	set       policyregistry.SelectionSetV2
	setRef    policyregistry.ObjectRef
}

func (f v2Fixture) selection(profileKey string) policyregistry.ServingSelection {
	if profileKey == "" {
		return policyregistry.ServingSelection{Release: f.set.Default.Candidate, Binding: f.setRef}
	}
	profile := f.setRef
	return policyregistry.ServingSelection{Release: f.set.Profiles[profileKey].Candidate, Binding: f.setRef, Profile: &profile}
}

// foldCandidate publishes the v2 candidate equivalent to a v1 release and its classifier bundle.
func foldCandidate(t *testing.T, store *servingMemoryStore, releaseRef policyregistry.ObjectRef) policyregistry.ObjectRef {
	t.Helper()
	release := store.object(t, policyregistry.ServingReleases, releaseRef).(*policyregistry.ServingRelease)
	bundle := store.object(t, policyregistry.ServingClassifiers, release.Classifier).(*policyregistry.ClassifierBundle)
	candidate := policyregistry.CandidateV2{SchemaVersion: policyregistry.ServingCandidateV2, CandidateComposition: policyregistry.CandidateComposition{
		RouterImageDigest: release.RouterImageDigest,
		Policy:            release.Policy,
		Classifier:        policyregistry.ClassifierComponent{Identity: bundle.Identity, Package: bundle.Package, AuxiliaryModels: bundle.AuxiliaryModels, Configuration: bundle.Configuration},
		Requirements:      release.Requirements,
		Provenance:        release.Provenance,
	}}
	return store.publishArtifact(t, policyregistry.ServingCandidate, candidate)
}

// foldLane expresses a v1 selection (release + binding + optional profile objects) as a v2 lane.
func foldLane(t *testing.T, store *servingMemoryStore, selection policyregistry.ServingSelection) policyregistry.ServingLane {
	t.Helper()
	binding := store.object(t, policyregistry.ServingBindings, selection.Binding).(*policyregistry.DeploymentBinding)
	lane := policyregistry.ServingLane{Candidate: foldCandidate(t, store, selection.Release), LaneBinding: policyregistry.LaneBinding{Project: binding.Project, Region: binding.Region, Router: binding.Router, Classifier: binding.Classifier, Attestation: binding.Attestation}}
	if selection.Profile != nil {
		profile := store.object(t, policyregistry.ServingProfiles, *selection.Profile).(*policyregistry.RoutingProfile)
		policy, requirements := profile.Policy, profile.Requirements
		lane.ProfileKey, lane.ProfilePolicy, lane.ProfileRequirements = profile.ProfileKey, &policy, &requirements
	}
	return lane
}

// foldSelectionSet publishes the v2 selection set equivalent to a v1 set whose objects are stored.
func foldSelectionSet(t *testing.T, store *servingMemoryStore, set policyregistry.SelectionSet) (policyregistry.SelectionSetV2, policyregistry.ObjectRef) {
	t.Helper()
	v2 := policyregistry.SelectionSetV2{SchemaVersion: policyregistry.ServingSelectionSetV2, Target: set.Target, Default: foldLane(t, store, set.Default), Profiles: make(map[string]policyregistry.ServingLane, len(set.Profiles))}
	for key, selection := range set.Profiles {
		v2.Profiles[key] = foldLane(t, store, selection)
	}
	return v2, store.publishArtifact(t, policyregistry.ServingSelectionSet, v2)
}

// foldProposal republishes a v1 proposal as v2: its selection set and source fold to artifacts/
// while previous_selection_set keeps whatever (possibly v1) reference the history recorded.
func foldProposal(t *testing.T, store *servingMemoryStore, proposal policyregistry.DeploymentProposal) (policyregistry.DeploymentProposalV2, policyregistry.ObjectRef) {
	t.Helper()
	set := store.object(t, policyregistry.ServingSelectionSets, proposal.SelectionSet).(*policyregistry.SelectionSet)
	_, setRef := foldSelectionSet(t, store, *set)
	v2 := policyregistry.DeploymentProposalV2{
		SchemaVersion:        policyregistry.ServingProposalV2,
		Target:               proposal.Target,
		PreviousSelectionSet: proposal.PreviousSelectionSet,
		SelectionSet:         setRef,
		SourceCandidate:      foldCandidate(t, store, proposal.SourceRelease),
		Scope:                proposal.Scope,
		ProfileKey:           proposal.ProfileKey,
		Actor:                proposal.Actor,
		Reason:               proposal.Reason,
		RequestID:            proposal.RequestID,
		CreatedAt:            proposal.CreatedAt,
		Evidence:             proposal.Evidence,
		WithdrawActivations:  proposal.WithdrawActivations,
	}
	return v2, store.publishArtifact(t, policyregistry.ServingProposal, v2)
}

func newV2Fixture(t *testing.T, store *servingMemoryStore, set policyregistry.SelectionSet) v2Fixture {
	t.Helper()
	release := store.object(t, policyregistry.ServingReleases, set.Default.Release).(*policyregistry.ServingRelease)
	set.Profiles[profileKeyOne] = registerProfileFixture(t, store, set.Default, profileKeyOne, release.Policy)
	store.publish(t, policyregistry.ServingSelectionSets, set)
	v2, setRef := foldSelectionSet(t, store, set)
	return v2Fixture{v1: set, candidate: v2.Default.Candidate, set: v2, setRef: setRef}
}

func fixtureProposalV2(fixture v2Fixture, previous *policyregistry.ObjectRef) policyregistry.DeploymentProposalV2 {
	return policyregistry.DeploymentProposalV2{SchemaVersion: policyregistry.ServingProposalV2, Target: fixture.set.Target, PreviousSelectionSet: previous, SelectionSet: fixture.setRef, SourceCandidate: fixture.candidate, Scope: policyregistry.ChangeFull, Actor: "test-operator", Reason: "release validation", RequestID: uuid.NewString(), CreatedAt: servingEpoch, Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{}}
}

func TestServingV2ContractsValidateFoldedShapes(t *testing.T) {
	store, _, set := controllerFixture(t)
	fixture := newV2Fixture(t, store, set)
	candidate := *store.object(t, policyregistry.ServingCandidate, fixture.candidate).(*policyregistry.CandidateV2)
	require.NoError(t, candidate.Validate(testRegistryRoot))
	require.NoError(t, fixture.set.Validate(testRegistryRoot))
	previous := namespaceRef(policyregistry.ServingSelectionSets, "incumbent")
	require.NoError(t, fixtureProposalV2(fixture, &previous).Validate(testRegistryRoot))

	t.Run("candidate", func(t *testing.T) {
		wrongSchema := candidate
		wrongSchema.SchemaVersion = policyregistry.ServingReleaseV1
		assert.ErrorContains(t, wrongSchema.Validate(testRegistryRoot), "schema")
		mismatched := candidate
		mismatched.Requirements.TaxonomySHA256 = strings.Repeat("9", 64)
		assert.ErrorContains(t, mismatched.Validate(testRegistryRoot), "classifier identity")
		wrongPackage := candidate
		wrongPackage.Classifier.Package = artifactRef("other-package")
		assert.ErrorContains(t, wrongPackage.Validate(testRegistryRoot), "attested identity")
		noInventory := candidate
		noInventory.Classifier.AuxiliaryModels = nil
		assert.ErrorContains(t, noInventory.Validate(testRegistryRoot), "auxiliary")
	})
	t.Run("selection set", func(t *testing.T) {
		defaultWithProfile := fixture.set
		defaultWithProfile.Default = fixture.set.Profiles[profileKeyOne]
		assert.ErrorContains(t, defaultWithProfile.Validate(testRegistryRoot), "default lane")
		bareProfile := fixture.set
		bareProfile.Profiles = map[string]policyregistry.ServingLane{profileKeyOne: fixture.set.Default}
		assert.ErrorContains(t, bareProfile.Validate(testRegistryRoot), "profile lane")
		renamed := fixture.set
		renamed.Profiles = map[string]policyregistry.ServingLane{profileKeyTwo: fixture.set.Profiles[profileKeyOne]}
		assert.ErrorContains(t, renamed.Validate(testRegistryRoot), "own key")
		noProfiles := fixture.set
		noProfiles.Profiles = nil
		assert.Error(t, noProfiles.Validate(testRegistryRoot))
		v1Candidate := fixture.set
		v1Candidate.Default.Candidate = namespaceRef(policyregistry.ServingBindings, "binding")
		assert.ErrorContains(t, v1Candidate.Validate(testRegistryRoot), "namespace")
		noRegion := fixture.set
		noRegion.Default.Region = ""
		assert.ErrorContains(t, noRegion.Validate(testRegistryRoot), "region")
	})
	t.Run("proposal", func(t *testing.T) {
		rollback := fixtureProposalV2(fixture, nil)
		rollback.Scope = policyregistry.ChangeRollback
		assert.ErrorContains(t, rollback.Validate(testRegistryRoot), "rollback")
		longID := fixtureProposalV2(fixture, nil)
		longID.RequestID = strings.Repeat("r", policyregistry.MaxServingRequestIDLength+1)
		assert.ErrorContains(t, longID.Validate(testRegistryRoot), "request ID")
		opaqueID := fixtureProposalV2(fixture, nil)
		opaqueID.RequestID = "workflow-run-42/attempt-1"
		assert.NoError(t, opaqueID.Validate(testRegistryRoot))
		wrongSource := fixtureProposalV2(fixture, nil)
		wrongSource.SourceCandidate = namespaceRef(policyregistry.ServingBindings, "binding")
		assert.ErrorContains(t, wrongSource.Validate(testRegistryRoot), "namespace")
		v1Source := fixtureProposalV2(fixture, nil)
		v1Source.SourceCandidate = set.Default.Release
		assert.NoError(t, v1Source.Validate(testRegistryRoot), "a v2 proposal may promote a v1 release as its source")
		profileScope := fixtureProposalV2(fixture, &previous)
		profileScope.Scope = policyregistry.ChangeProfile
		assert.Error(t, profileScope.Validate(testRegistryRoot))
		profileScope.ProfileKey = profileKeyOne
		assert.NoError(t, profileScope.Validate(testRegistryRoot))
	})
	t.Run("no generation is serialized", func(t *testing.T) {
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(servingPayload(t, fixtureProposalV2(fixture, nil)), &fields))
		assert.NotContains(t, fields, "expected_generation")
		assert.Contains(t, fields, "source_candidate")
		assert.NotContains(t, fields, "source_release")
	})
}

func TestServingRefAcceptsBothLayoutsOnlyForTheKindThatFoldsThem(t *testing.T) {
	artifact := artifactStoredRef([]byte("object"))
	for _, kind := range []policyregistry.ServingKind{policyregistry.ServingCandidate, policyregistry.ServingSelectionSet, policyregistry.ServingProposal} {
		assert.NoError(t, policyregistry.ValidateServingRef(artifact, testRegistryRoot, kind), kind)
	}
	for _, kind := range []policyregistry.ServingKind{policyregistry.ServingReleases, policyregistry.ServingClassifiers, policyregistry.ServingBindings, policyregistry.ServingProfiles, policyregistry.ServingSelectionSets, policyregistry.ServingProposals} {
		assert.ErrorContains(t, policyregistry.ValidateServingRef(artifact, testRegistryRoot, kind), "namespace", kind)
	}
	folds := map[policyregistry.ServingKind]policyregistry.ServingKind{policyregistry.ServingCandidate: policyregistry.ServingReleases, policyregistry.ServingSelectionSet: policyregistry.ServingSelectionSets, policyregistry.ServingProposal: policyregistry.ServingProposals}
	for family, legacy := range folds {
		assert.NoError(t, policyregistry.ValidateServingRef(namespaceRef(legacy, "object"), testRegistryRoot, family))
		for _, other := range []policyregistry.ServingKind{policyregistry.ServingReleases, policyregistry.ServingClassifiers, policyregistry.ServingBindings, policyregistry.ServingProfiles, policyregistry.ServingSelectionSets, policyregistry.ServingProposals} {
			if other != legacy {
				assert.Error(t, policyregistry.ValidateServingRef(namespaceRef(other, "object"), testRegistryRoot, family), "%s must not accept %s", family, other)
			}
		}
	}
	outside := artifact
	outside.URI = "gs://weave_ml/other_registry/artifacts/" + artifact.SHA256 + ".json"
	assert.Error(t, policyregistry.ValidateServingRef(outside, testRegistryRoot, policyregistry.ServingCandidate))
	relabeled := artifact
	relabeled.URI = testRegistryRoot + "/artifacts/" + strings.Repeat("0", 64) + ".json"
	assert.Error(t, policyregistry.ValidateServingRef(relabeled, testRegistryRoot, policyregistry.ServingCandidate), "the URI must name the referenced digest")
}

func TestDecodeServingObjectDispatchesOnLayoutThenSchema(t *testing.T) {
	store, _, set := controllerFixture(t)
	fixture := newV2Fixture(t, store, set)
	release := store.object(t, policyregistry.ServingReleases, set.Default.Release)
	releasePayload := store.objects[set.Default.Release]
	candidatePayload := store.objects[fixture.candidate]

	legacyAsFamily, err := policyregistry.DecodeServingObject(releasePayload, testRegistryRoot, policyregistry.ServingCandidate, set.Default.Release)
	require.NoError(t, err)
	assert.Equal(t, release, legacyAsFamily, "a candidate reference to the legacy namespace decodes the v1 release")
	artifact, err := policyregistry.DecodeServingObject(candidatePayload, testRegistryRoot, policyregistry.ServingCandidate, fixture.candidate)
	require.NoError(t, err)
	assert.IsType(t, &policyregistry.CandidateV2{}, artifact)

	_, err = policyregistry.DecodeServingManifest(candidatePayload, testRegistryRoot, policyregistry.ServingReleases)
	assert.Error(t, err, "a v1 kind never decodes a v2 schema")
	_, err = policyregistry.DecodeServingObject(candidatePayload, testRegistryRoot, policyregistry.ServingCandidate, servingStoredRef(t, policyregistry.ServingReleases, candidatePayload))
	assert.ErrorContains(t, err, "storage layout", "v2 bytes cannot live under a legacy namespace")
	_, err = policyregistry.DecodeServingObject(releasePayload, testRegistryRoot, policyregistry.ServingCandidate, artifactStoredRef(releasePayload))
	assert.ErrorContains(t, err, "storage layout", "v1 bytes cannot live under artifacts/")
	_, err = policyregistry.DecodeServingObject(candidatePayload, testRegistryRoot, policyregistry.ServingSelectionSet, fixture.candidate)
	assert.ErrorContains(t, err, "not a selection_set manifest", "the family kind must agree with the declared schema")
	_, err = policyregistry.DecodeServingObject(store.objects[set.Default.Binding], testRegistryRoot, policyregistry.ServingCandidate, artifactStoredRef(store.objects[set.Default.Binding]))
	assert.Error(t, err, "unfolded v1 kinds have no artifacts/ representation")
	_, err = policyregistry.DecodeServingManifest([]byte(`{"target":"prod/stable"}`), testRegistryRoot, policyregistry.ServingProposal)
	assert.ErrorContains(t, err, "schema_version")

	for _, kind := range []policyregistry.ServingKind{policyregistry.ServingCandidate, policyregistry.ServingSelectionSet} {
		ref := map[policyregistry.ServingKind]policyregistry.ObjectRef{policyregistry.ServingCandidate: fixture.candidate, policyregistry.ServingSelectionSet: fixture.setRef}[kind]
		manifest, payload, err := store.ReadServingObject(context.Background(), kind, ref)
		require.NoError(t, err)
		assert.Equal(t, ref.SHA256, policyregistry.Digest(payload))
		reencoded, err := policyregistry.DecodeServingObject(driftedPayload(t, payload), testRegistryRoot, kind, ref)
		require.NoError(t, err)
		assert.Equal(t, manifest, reencoded, "encoding is not part of the v2 contract either")
	}
}

func TestPreparedSelectionNormalizesV1TraversalAndV2LanesIdentically(t *testing.T) {
	store, _, set := controllerFixture(t)
	fixture := newV2Fixture(t, store, set)
	ctx := context.Background()
	for _, profileKey := range []string{"", profileKeyOne} {
		v1Selection := fixture.v1.Default
		if profileKey != "" {
			v1Selection = fixture.v1.Profiles[profileKey]
		}
		v2Selection := fixture.selection(profileKey)
		require.NotEqual(t, v1Selection, v2Selection)
		v1, err := policyregistry.ReadPreparedSelection(ctx, store, policyregistry.TargetStable, profileKey, v1Selection)
		require.NoError(t, err, profileKey)
		v2, err := policyregistry.ReadPreparedSelection(ctx, store, policyregistry.TargetStable, profileKey, v2Selection)
		require.NoError(t, err, profileKey)
		assert.Equal(t, v1Selection, v1.Selection)
		assert.Equal(t, v2Selection, v2.Selection)
		v1.Selection, v2.Selection = policyregistry.ServingSelection{}, policyregistry.ServingSelection{}
		assert.Equal(t, v1, v2, "same composition expressed both ways must normalize identically (profile %q)", profileKey)
		assert.NotEmpty(t, v2.Candidate.Classifier.AuxiliaryModels)
		assert.Equal(t, profileKey, v2.ProfileKey)
		assert.Same(t, v1.Policy, v2.Policy)

		v1Binding, err := policyregistry.ResolveAdmissionBinding(ctx, store, policyregistry.SessionReleaseBinding{Target: policyregistry.TargetStable, ProfileKey: profileKey, Selection: v1Selection})
		require.NoError(t, err)
		v2Binding, err := policyregistry.ResolveAdmissionBinding(ctx, store, policyregistry.SessionReleaseBinding{Target: policyregistry.TargetStable, ProfileKey: profileKey, Selection: v2Selection})
		require.NoError(t, err)
		assert.Equal(t, v1Binding, v2Binding)
		assert.Equal(t, v2.Binding, v2Binding)
	}

	t.Run("lane selections are cross-checked against the embedded set", func(t *testing.T) {
		otherCandidate := fixture.selection("")
		otherCandidate.Release = artifactStoredRef([]byte("another candidate"))
		_, err := policyregistry.ReadPreparedSelection(ctx, store, policyregistry.TargetStable, "", otherCandidate)
		assert.ErrorContains(t, err, "no lane")
		unknownProfile := fixture.selection(profileKeyOne)
		_, err = policyregistry.ReadPreparedSelection(ctx, store, policyregistry.TargetStable, profileKeyTwo, unknownProfile)
		assert.ErrorContains(t, err, "no lane")
		otherTarget := fixture.selection("")
		_, err = policyregistry.ReadPreparedSelection(ctx, store, policyregistry.TargetStaging, "", otherTarget)
		assert.ErrorContains(t, err, "another target")
		foreignProfile := fixture.selection(profileKeyOne)
		v1Profile := *fixture.v1.Profiles[profileKeyOne].Profile
		foreignProfile.Profile = &v1Profile
		_, err = policyregistry.ReadPreparedSelection(ctx, store, policyregistry.TargetStable, profileKeyOne, foreignProfile)
		assert.ErrorContains(t, err, "embeds the lane")
		_, err = policyregistry.ResolveAdmissionBinding(ctx, store, policyregistry.SessionReleaseBinding{Target: policyregistry.TargetStable, Selection: otherCandidate})
		assert.ErrorContains(t, err, "no lane")
	})
	t.Run("lane binding must realize the candidate", func(t *testing.T) {
		drifted := fixture.set
		drifted.Default.Router.ImageDigest = "sha256:" + strings.Repeat("f", 64)
		driftedRef := store.publishArtifact(t, policyregistry.ServingSelectionSet, drifted)
		selection := policyregistry.ServingSelection{Release: fixture.candidate, Binding: driftedRef}
		_, err := policyregistry.ReadPreparedSelection(ctx, store, policyregistry.TargetStable, "", selection)
		assert.ErrorContains(t, err, "differs from release or classifier")
	})
	t.Run("profile lane must pin the candidate policy", func(t *testing.T) {
		drifted := fixture.set
		lane := fixture.set.Profiles[profileKeyOne]
		policy := *lane.ProfilePolicy
		policy.SHA256 = strings.Repeat("e", 64)
		policy.URI = testRegistryRoot + "/router_policy/v1/policies/sha256/" + policy.SHA256 + ".json"
		lane.ProfilePolicy = &policy
		drifted.Profiles = map[string]policyregistry.ServingLane{profileKeyOne: lane}
		driftedRef := store.publishArtifact(t, policyregistry.ServingSelectionSet, drifted)
		selection := policyregistry.ServingSelection{Release: fixture.candidate, Binding: driftedRef, Profile: &driftedRef}
		_, err := policyregistry.ReadPreparedSelection(ctx, store, policyregistry.TargetStable, profileKeyOne, selection)
		assert.ErrorContains(t, err, "immutable policy")
	})
}

func TestGCSReadServingStatePrefersNewPathAndFallsBackToLegacy(t *testing.T) {
	registry, fixture := newServingGCSFixture(t)
	ctx := context.Background()
	set := fixtureSet("active")
	legacySnapshot, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	newSnapshot, _ := activateFixture(t, legacySnapshot, fixtureSet("next"), servingEpoch.Add(time.Hour))
	legacyName := "weave_registry/runtime_state/router_serving/v1/targets/prod-01/prod/stable/state.json"
	newName := "weave_registry/state/prod-01/prod/stable.json"
	seed := func(name string, state policyregistry.ServingControlState) int64 {
		payload, err := json.Marshal(state)
		require.NoError(t, err)
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.generation++
		fixture.objects[name] = map[int64][]byte{fixture.generation: payload}
		fixture.current[name] = fixture.generation
		return fixture.generation
	}

	_, err := registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.ErrorIs(t, err, policyregistry.ErrNotFound)

	legacyGeneration := seed(legacyName, legacySnapshot.State)
	observed, err := registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.NoError(t, err)
	assert.Equal(t, policyregistry.ServingStateSnapshot{State: legacySnapshot.State, Generation: legacyGeneration, LegacyPath: true}, observed)

	newGeneration := seed(newName, newSnapshot.State)
	observed, err = registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.NoError(t, err)
	assert.Equal(t, policyregistry.ServingStateSnapshot{State: newSnapshot.State, Generation: newGeneration}, observed, "the new path is authoritative even while the legacy object still exists")

	fixture.mu.Lock()
	fixture.objects[newName] = map[int64][]byte{fixture.current[newName]: []byte(`{"schema_version":"router_serving_control_state_v1"}`)}
	fixture.mu.Unlock()
	_, err = registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.Error(t, err, "an invalid new-path object is a failure, never a silent fall back to legacy state")
}
