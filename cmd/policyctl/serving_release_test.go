package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

// cliReleaseTriple expresses the fixture's v1 composition as the three v2 manifests a release
// publishes, each already naming the reference the next one expects.
type cliReleaseTriple struct {
	candidate       policyregistry.CandidateV2
	candidateRef    policyregistry.ObjectRef
	selectionSet    policyregistry.SelectionSetV2
	selectionSetRef policyregistry.ObjectRef
	proposal        policyregistry.DeploymentProposalV2
}

func cliArtifactRef(t *testing.T, manifest policyregistry.ServingManifest) policyregistry.ObjectRef {
	t.Helper()
	payload, err := policyregistry.CanonicalBytes(manifest)
	require.NoError(t, err)
	digest := policyregistry.Digest(payload)
	return policyregistry.ObjectRef{URI: defaultRegistryURI + "/artifacts/" + digest + ".json", SHA256: digest, Generation: 1}
}

func cliObject[T policyregistry.ServingManifest](t *testing.T, registry *cliServingRegistry, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) T {
	t.Helper()
	manifest, _, err := registry.ReadServingObject(context.Background(), kind, ref)
	require.NoError(t, err)
	typed, ok := manifest.(T)
	require.True(t, ok, "fixture object %s is not a %T", ref.SHA256, *new(T))
	return typed
}

func cliReleaseFixture(t *testing.T, registry *cliServingRegistry, v1 policyregistry.DeploymentProposal) cliReleaseTriple {
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
	candidateRef := cliArtifactRef(t, candidate)
	selectionSet := policyregistry.SelectionSetV2{SchemaVersion: policyregistry.ServingSelectionSetV2, Target: set.Target, Profiles: map[string]policyregistry.ServingLane{}, Default: policyregistry.ServingLane{
		Candidate:   candidateRef,
		LaneBinding: policyregistry.LaneBinding{Project: binding.Project, Region: binding.Region, Router: binding.Router, Classifier: binding.Classifier, Attestation: binding.Attestation},
	}}
	selectionSetRef := cliArtifactRef(t, selectionSet)
	proposal := policyregistry.DeploymentProposalV2{
		SchemaVersion: policyregistry.ServingProposalV2, Target: set.Target, SelectionSet: selectionSetRef,
		SourceCandidate: candidateRef, Scope: policyregistry.ChangeFull, Actor: "release-operator", Reason: "publish release",
		RequestID: "run-1:lane-0", CreatedAt: v1.CreatedAt, Evidence: v1.Evidence, WithdrawActivations: []string{},
	}
	return cliReleaseTriple{candidate: candidate, candidateRef: candidateRef, selectionSet: selectionSet, selectionSetRef: selectionSetRef, proposal: proposal}
}

// cliReleaseArgs writes the triple to files and returns the invocation that publishes them.
func cliReleaseArgs(t *testing.T, triple cliReleaseTriple) []string {
	t.Helper()
	directory := t.TempDir()
	paths := make([]string, 0, 3)
	for index, manifest := range []policyregistry.ServingManifest{triple.candidate, triple.selectionSet, triple.proposal} {
		payload, err := policyregistry.CanonicalBytes(manifest)
		require.NoError(t, err)
		path := filepath.Join(directory, []string{"candidate", "selection_set", "proposal"}[index]+".json")
		require.NoError(t, os.WriteFile(path, payload, 0o600))
		paths = append(paths, path)
	}
	return []string{string(commandPublishRelease), "--candidate", paths[0], "--selection-set", paths[1], "--proposal", paths[2]}
}

func TestServingCLIPublishReleaseChainsThreePublishesIdempotently(t *testing.T) {
	registry, _, v1 := cliServingFixture(t)
	triple := cliReleaseFixture(t, registry, v1)
	args := cliReleaseArgs(t, triple)
	var output any
	dependencies := cliDependencies(registry, nil, &output, nil)
	ctx := context.Background()
	seeded := registry.objectWrites

	require.NoError(t, runServingWith(ctx, args, dependencies))
	first := output.(servingReleaseRefs)
	require.Equal(t, triple.candidateRef, first.Candidate)
	require.Equal(t, triple.selectionSetRef, first.SelectionSet)
	require.Equal(t, policyregistry.Digest(registry.objects[first.Proposal]), first.Proposal.SHA256)
	require.Equal(t, seeded+3, registry.objectWrites, "the release publishes exactly the candidate, selection set and proposal")
	for _, ref := range []policyregistry.ObjectRef{first.Candidate, first.SelectionSet, first.Proposal} {
		require.Contains(t, registry.objects, ref, "every returned reference addresses stored bytes")
		require.Equal(t, policyregistry.Digest(registry.objects[ref]), ref.SHA256)
	}
	require.Zero(t, registry.writes, "publishing a release never touches target state")

	output = nil
	require.NoError(t, runServingWith(ctx, args, dependencies))
	require.Equal(t, first, output.(servingReleaseRefs), "a re-run reports the already-published references")
	require.Equal(t, seeded+3, registry.objectWrites, "a re-run issues no new writes")
}

// A partially published release resumes: the candidate exists, the rest is still created.
func TestServingCLIPublishReleaseResumesAfterPartialPublish(t *testing.T) {
	registry, _, v1 := cliServingFixture(t)
	triple := cliReleaseFixture(t, registry, v1)
	args := cliReleaseArgs(t, triple)
	var output any
	dependencies := cliDependencies(registry, nil, &output, nil)
	ctx := context.Background()
	candidatePayload, err := policyregistry.CanonicalBytes(triple.candidate)
	require.NoError(t, err)
	published, err := registry.PublishServingManifest(ctx, policyregistry.ServingCandidate, candidatePayload)
	require.NoError(t, err)
	require.Equal(t, triple.candidateRef, published)
	seeded := registry.objectWrites

	require.NoError(t, runServingWith(ctx, args, dependencies))
	refs := output.(servingReleaseRefs)
	require.Equal(t, triple.candidateRef, refs.Candidate)
	require.Equal(t, seeded+2, registry.objectWrites, "only the objects still missing are written")
}

func TestServingCLIPublishReleaseRejectsMismatchedReferencesBeforeWriting(t *testing.T) {
	stale := policyregistry.ObjectRef{URI: defaultRegistryURI + "/artifacts/" + strings.Repeat("a", 64) + ".json", SHA256: strings.Repeat("a", 64), Generation: 1}
	for _, scenario := range []struct {
		name      string
		mutate    func(*cliReleaseTriple)
		republish bool
		contains  string
		writes    int
	}{
		{
			name:      "lane names another candidate",
			mutate:    func(triple *cliReleaseTriple) { triple.selectionSet.Default.Candidate = stale },
			republish: true,
			contains:  "no selection set lane references the candidate",
		},
		{
			name:     "proposal names another selection set",
			mutate:   func(triple *cliReleaseTriple) { triple.proposal.SelectionSet = stale },
			contains: "proposal names selection set",
		},
		{
			name:     "proposal names another source candidate",
			mutate:   func(triple *cliReleaseTriple) { triple.proposal.SourceCandidate = stale },
			contains: "proposal names source candidate",
		},
		{
			name: "lane names the published candidate at the wrong generation",
			mutate: func(triple *cliReleaseTriple) {
				triple.selectionSet.Default.Candidate.Generation = 9
				triple.proposal.SourceCandidate.Generation = 9
			},
			republish: true,
			contains:  "at generation 9, but the candidate published as",
			writes:    1,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			registry, _, v1 := cliServingFixture(t)
			triple := cliReleaseFixture(t, registry, v1)
			scenario.mutate(&triple)
			if scenario.republish {
				triple.selectionSetRef = cliArtifactRef(t, triple.selectionSet)
				triple.proposal.SelectionSet = triple.selectionSetRef
			}
			var output any
			dependencies := cliDependencies(registry, nil, &output, nil)
			seeded := registry.objectWrites

			err := runServingWith(context.Background(), cliReleaseArgs(t, triple), dependencies)
			require.ErrorContains(t, err, scenario.contains)
			require.Nil(t, output, "a rejected release prints nothing")
			require.Equal(t, seeded+scenario.writes, registry.objectWrites, "the release stops before writing the mismatched manifest")
		})
	}
}

func TestServingCLIPublishReleaseDryRunReportsDigestsWithoutOpeningTheRegistry(t *testing.T) {
	registry, _, v1 := cliServingFixture(t)
	triple := cliReleaseFixture(t, registry, v1)
	opened := 0
	var output any
	dependencies := cliDependencies(registry, nil, &output, nil)
	dependencies.openRegistry = func(context.Context, string) (servingRegistry, error) { opened++; return registry, nil }
	seeded := registry.objectWrites

	require.NoError(t, runServingWith(context.Background(), append(cliReleaseArgs(t, triple), "--dry-run"), dependencies))
	encoded, err := json.Marshal(output)
	require.NoError(t, err)
	require.JSONEq(t, `{"candidate":{"kind":"candidate","sha256":"`+triple.candidateRef.SHA256+`"},"selection_set":{"kind":"selection_set","sha256":"`+triple.selectionSetRef.SHA256+`"},"proposal":{"kind":"proposal","sha256":"`+cliArtifactRef(t, triple.proposal).SHA256+`"}}`, string(encoded))
	require.Zero(t, opened, "a dry run never opens the registry")
	require.Equal(t, seeded, registry.objectWrites, "a dry run writes nothing")
}

func TestServingCLIPublishReleaseRequiresEveryManifestFlag(t *testing.T) {
	registry, _, v1 := cliServingFixture(t)
	triple := cliReleaseFixture(t, registry, v1)
	args := cliReleaseArgs(t, triple)
	var output any
	dependencies := cliDependencies(registry, nil, &output, nil)
	ctx := context.Background()
	seeded := registry.objectWrites

	require.ErrorContains(t, runServingWith(ctx, args[:5], dependencies), "requires --candidate, --selection-set and --proposal")
	require.ErrorContains(t, runServingWith(ctx, append(args, "--kind", "candidate"), dependencies), "flag provided but not defined: -kind")
	require.Equal(t, seeded, registry.objectWrites)
	require.Nil(t, output)
}
