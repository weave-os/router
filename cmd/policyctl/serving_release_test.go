package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

// cliReleaseFiles are the three manifests a release publishes, as the caller writes them: the
// candidate is complete, while the references to objects that do not exist yet are left for
// `publish-release` to fill in.
type cliReleaseFiles struct {
	candidate    policyregistry.CandidateV2
	selectionSet map[string]any
	proposal     map[string]any
}

func cliObject[T policyregistry.ServingManifest](t *testing.T, registry *cliServingRegistry, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) T {
	t.Helper()
	manifest, _, err := registry.ReadServingObject(context.Background(), kind, ref)
	require.NoError(t, err)
	typed, ok := manifest.(T)
	require.True(t, ok, "fixture object %s is not a %T", ref.SHA256, *new(T))
	return typed
}

func cliDocument(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	document := map[string]any{}
	require.NoError(t, json.Unmarshal(encoded, &document))
	return document
}

// cliReleaseFixture rebuilds the fixture's v1 composition as a v2 release whose selection set and
// proposal carry no usable generations, which is the only shape a caller can write before the
// candidate exists.
func cliReleaseFixture(t *testing.T, registry *cliServingRegistry, v1 policyregistry.DeploymentProposal) cliReleaseFiles {
	t.Helper()
	release := cliObject[*policyregistry.ServingRelease](t, registry, policyregistry.ServingReleases, v1.SourceRelease)
	bundle := cliObject[*policyregistry.ClassifierBundle](t, registry, policyregistry.ServingClassifiers, release.Classifier)
	set := cliObject[*policyregistry.SelectionSet](t, registry, policyregistry.ServingSelectionSets, v1.SelectionSet)
	binding := cliObject[*policyregistry.DeploymentBinding](t, registry, policyregistry.ServingBindings, set.Default.Binding)
	candidate := policyregistry.CandidateV2{SchemaVersion: policyregistry.ServingCandidateV2, CandidateComposition: policyregistry.CandidateComposition{
		RouterImageDigest: release.RouterImageDigest,
		Policy:            release.Policy,
		Classifier:        policyregistry.ClassifierComponent{Identity: bundle.Identity, Package: bundle.Package, AuxiliaryModels: bundle.AuxiliaryModels, Configuration: bundle.Configuration},
		Requirements:      release.Requirements,
		Provenance:        release.Provenance,
	}}
	lane := policyregistry.ServingLane{LaneBinding: policyregistry.LaneBinding{Project: binding.Project, Region: binding.Region, Router: binding.Router, Classifier: binding.Classifier, Attestation: binding.Attestation}}
	selectionSet := cliDocument(t, policyregistry.SelectionSetV2{SchemaVersion: policyregistry.ServingSelectionSetV2, Target: set.Target, Default: lane, Profiles: map[string]policyregistry.ServingLane{}})
	proposal := cliDocument(t, policyregistry.DeploymentProposalV2{
		SchemaVersion: policyregistry.ServingProposalV2, Target: set.Target, Scope: policyregistry.ChangeFull,
		Actor: "release-operator", Reason: "publish release", RequestID: "run-1:lane-0",
		CreatedAt: v1.CreatedAt, Evidence: v1.Evidence, WithdrawActivations: []string{},
	})
	return cliReleaseFiles{candidate: candidate, selectionSet: selectionSet, proposal: proposal}
}

// cliReleaseArgs writes the three manifests and returns the invocation that publishes them.
func cliReleaseArgs(t *testing.T, files cliReleaseFiles) []string {
	t.Helper()
	directory := t.TempDir()
	paths := make([]string, 0, 3)
	for index, manifest := range []any{files.candidate, files.selectionSet, files.proposal} {
		payload, err := json.Marshal(manifest)
		require.NoError(t, err)
		path := filepath.Join(directory, []string{"candidate", "selection_set", "proposal"}[index]+".json")
		require.NoError(t, os.WriteFile(path, payload, 0o600))
		paths = append(paths, path)
	}
	return []string{string(commandPublishRelease), "--candidate", paths[0], "--selection-set", paths[1], "--proposal", paths[2]}
}

func cliCandidateDigest(t *testing.T, files cliReleaseFiles) string {
	t.Helper()
	payload, err := json.Marshal(files.candidate)
	require.NoError(t, err)
	return policyregistry.Digest(payload)
}

// A fresh release is publishable in one invocation: the caller cannot know the generation GCS
// assigns, so the lane and proposal references come from the objects actually published.
func TestServingCLIPublishReleaseFillsReferencesFromWhatItPublished(t *testing.T) {
	registry, _, v1 := cliServingFixture(t)
	files := cliReleaseFixture(t, registry, v1)
	args := cliReleaseArgs(t, files)
	var output any
	dependencies := cliDependencies(registry, nil, &output, nil)
	ctx := context.Background()
	seeded := registry.objectWrites

	require.NoError(t, runServingWith(ctx, args, dependencies))
	refs := output.(servingReleaseRefs)
	require.Equal(t, cliCandidateDigest(t, files), refs.Candidate.SHA256)
	require.Equal(t, seeded+3, registry.objectWrites, "the release publishes exactly the candidate, selection set and proposal")
	for _, ref := range []policyregistry.ObjectRef{refs.Candidate, refs.SelectionSet, refs.Proposal} {
		require.Contains(t, registry.objects, ref, "every returned reference addresses stored bytes")
		require.Equal(t, policyregistry.Digest(registry.objects[ref]), ref.SHA256)
		require.GreaterOrEqual(t, ref.Generation, cliFirstGeneration, "the generation comes from the registry, not the input file")
	}
	published := cliObject[*policyregistry.SelectionSetV2](t, registry, policyregistry.ServingSelectionSet, refs.SelectionSet)
	require.Equal(t, refs.Candidate, published.Default.Candidate, "the published lane names the candidate that was just created")
	proposal := cliObject[*policyregistry.DeploymentProposalV2](t, registry, policyregistry.ServingProposal, refs.Proposal)
	require.Equal(t, refs.SelectionSet, proposal.SelectionSet)
	require.Equal(t, refs.Candidate, proposal.SourceCandidate)
	require.Zero(t, registry.writes, "publishing a release never touches target state")

	output = nil
	require.NoError(t, runServingWith(ctx, args, dependencies))
	require.Equal(t, refs, output.(servingReleaseRefs), "a re-run re-derives the same bytes and reports the same references")
	require.Equal(t, seeded+3, registry.objectWrites, "a re-run issues no new writes")
}

// A release that failed after the candidate was published resumes without republishing it.
func TestServingCLIPublishReleaseResumesAfterPartialPublish(t *testing.T) {
	registry, _, v1 := cliServingFixture(t)
	files := cliReleaseFixture(t, registry, v1)
	var output any
	dependencies := cliDependencies(registry, nil, &output, nil)
	ctx := context.Background()
	candidatePayload, err := json.Marshal(files.candidate)
	require.NoError(t, err)
	candidateRef, err := registry.PublishServingManifest(ctx, policyregistry.ServingCandidate, candidatePayload)
	require.NoError(t, err)
	seeded := registry.objectWrites

	require.NoError(t, runServingWith(ctx, cliReleaseArgs(t, files), dependencies))
	refs := output.(servingReleaseRefs)
	require.Equal(t, candidateRef, refs.Candidate)
	require.Equal(t, seeded+2, registry.objectWrites, "only the objects still missing are written")
}

// A lane or proposal that names a different object belongs to another release and is rejected
// rather than rewritten — before the manifest naming it is written.
func TestServingCLIPublishReleaseRejectsManifestsNamingAnotherRelease(t *testing.T) {
	stale := policyregistry.ObjectRef{URI: defaultRegistryURI + "/artifacts/" + strings.Repeat("a", 64) + ".json", SHA256: strings.Repeat("a", 64), Generation: 1}
	staleReference := map[string]any{"uri": stale.URI, "sha256": stale.SHA256, "generation": stale.Generation}
	for _, scenario := range []struct {
		name     string
		mutate   func(cliReleaseFiles)
		contains string
		writes   int
	}{
		{
			name:     "no lane names the candidate",
			mutate:   func(files cliReleaseFiles) { lane(files)["candidate"] = staleReference },
			contains: "no selection set lane references the candidate",
			writes:   1,
		},
		{
			name:     "proposal names another selection set",
			mutate:   func(files cliReleaseFiles) { files.proposal["selection_set"] = staleReference },
			contains: "proposal names selection_set " + stale.SHA256,
			writes:   2,
		},
		{
			name:     "proposal names another source candidate",
			mutate:   func(files cliReleaseFiles) { files.proposal["source_candidate"] = staleReference },
			contains: "proposal names source_candidate " + stale.SHA256,
			writes:   2,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			registry, _, v1 := cliServingFixture(t)
			files := cliReleaseFixture(t, registry, v1)
			scenario.mutate(files)
			var output any
			dependencies := cliDependencies(registry, nil, &output, nil)
			seeded := registry.objectWrites

			err := runServingWith(context.Background(), cliReleaseArgs(t, files), dependencies)
			require.ErrorContains(t, err, scenario.contains)
			require.Nil(t, output, "a rejected release prints nothing")
			require.Equal(t, seeded+scenario.writes, registry.objectWrites, "the release stops before writing the rejected manifest")
		})
	}
}

// A lane already pinned to a different, published candidate is left alone.
func TestServingCLIPublishReleaseLeavesLanesPinnedToAnotherCandidate(t *testing.T) {
	registry, _, v1 := cliServingFixture(t)
	files := cliReleaseFixture(t, registry, v1)
	pinned := cliReleaseFixture(t, registry, v1)
	pinned.candidate.Provenance.RouterRevision = strings.Repeat("e", 40)
	pinnedPayload, err := json.Marshal(pinned.candidate)
	require.NoError(t, err)
	pinnedRef, err := registry.PublishServingManifest(context.Background(), policyregistry.ServingCandidate, pinnedPayload)
	require.NoError(t, err)
	profileKey := uuid.NewString()
	profile := cliDocument(t, lane(files))
	profile["candidate"] = map[string]any{"uri": pinnedRef.URI, "sha256": pinnedRef.SHA256, "generation": pinnedRef.Generation}
	profile["profile_key"] = profileKey
	profile["profile_policy"] = cliDocument(t, files.candidate.Policy)
	profile["profile_requirements"] = cliDocument(t, files.candidate.Requirements)
	files.selectionSet["profiles"] = map[string]any{profileKey: profile}
	var output any
	dependencies := cliDependencies(registry, nil, &output, nil)

	require.NoError(t, runServingWith(context.Background(), cliReleaseArgs(t, files), dependencies))
	refs := output.(servingReleaseRefs)
	published := cliObject[*policyregistry.SelectionSetV2](t, registry, policyregistry.ServingSelectionSet, refs.SelectionSet)
	require.Equal(t, refs.Candidate, published.Default.Candidate)
	require.Equal(t, pinnedRef, published.Profiles[profileKey].Candidate, "a lane pinned to another candidate keeps it")
}

func TestServingCLIPublishReleaseDryRunValidatesWithoutOpeningTheRegistry(t *testing.T) {
	registry, _, v1 := cliServingFixture(t)
	files := cliReleaseFixture(t, registry, v1)
	opened := 0
	var output any
	dependencies := cliDependencies(registry, nil, &output, nil)
	dependencies.openRegistry = func(context.Context, string) (servingRegistry, error) { opened++; return registry, nil }
	seeded := registry.objectWrites

	require.NoError(t, runServingWith(context.Background(), append(cliReleaseArgs(t, files), "--dry-run"), dependencies))
	encoded, err := json.Marshal(output)
	require.NoError(t, err)
	require.JSONEq(t, `{"candidate":{"kind":"candidate","sha256":"`+cliCandidateDigest(t, files)+`"},"selection_set":{"kind":"selection_set","validated":true},"proposal":{"kind":"proposal","validated":true}}`, string(encoded), "only the candidate's digest is knowable before the release is published")
	require.Zero(t, opened, "a dry run never opens the registry")
	require.Equal(t, seeded, registry.objectWrites, "a dry run writes nothing")

	files.proposal["actor"] = ""
	output = nil
	require.ErrorContains(t, runServingWith(context.Background(), append(cliReleaseArgs(t, files), "--dry-run"), dependencies), "proposal manifest")
	require.Nil(t, output)
}

func TestServingCLIPublishReleaseRequiresEveryManifestFlag(t *testing.T) {
	registry, _, v1 := cliServingFixture(t)
	args := cliReleaseArgs(t, cliReleaseFixture(t, registry, v1))
	var output any
	dependencies := cliDependencies(registry, nil, &output, nil)
	ctx := context.Background()
	seeded := registry.objectWrites

	require.ErrorContains(t, runServingWith(ctx, args[:5], dependencies), "requires --candidate, --selection-set and --proposal")
	require.ErrorContains(t, runServingWith(ctx, append(args, "--kind", "candidate"), dependencies), "flag provided but not defined: -kind")
	require.Equal(t, seeded, registry.objectWrites)
	require.Nil(t, output)
}

func lane(files cliReleaseFiles) map[string]any {
	return files.selectionSet["default"].(map[string]any)
}
