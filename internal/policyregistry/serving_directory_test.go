package policyregistry_test

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

const selfHostedWorkerURL = "http://classifier-worker:8093"

type localV4Release struct {
	dir       string
	store     *servingMemoryStore
	selection policyregistry.SelectionSetV4
	ref       policyregistry.ObjectRef
	policy    policyregistry.PolicyObject
	v3Ref     policyregistry.ObjectRef
}

func writeLocalV4Release(t *testing.T) localV4Release {
	t.Helper()
	store, v3Set := cohortFixture(t)
	v3Set.Target = policyregistry.TargetInternal
	legacyCandidate := *store.object(t, policyregistry.ServingCandidate, v3Set.Code.Candidate).(*policyregistry.CandidateV2)
	defaultPolicy := publishRosterArm(t, store, legacyCandidate.Policy, alternateRosterArm)
	classOrder := slices.Clone(legacyCandidate.Classifier.Identity.ClassOrder)
	candidate := policyregistry.CandidateV3{
		SchemaVersion:     policyregistry.ServingCandidateV3,
		RouterImageDigest: "sha256:" + strings.Repeat("9", 64),
		Requirements: policyregistry.CandidateRequirementsV3{
			RuntimeContract:    policyregistry.ManagedRuntimeContractV1,
			PolicySchema:       rosterdata.SchemaVersionPolicyV1,
			SupportedAPIMajors: []policy.ClassifierAPIMajor{policy.ClassifierAPIMajorV5},
		},
		Provenance: policyregistry.ServingProvenance{
			RouterRevision: strings.Repeat("7", 40), WeaveRevision: strings.Repeat("8", 40),
			BuildAttestation: artifactRef("build-attestation"),
		},
	}
	candidateRef := store.publishArtifact(t, policyregistry.ServingCandidate, candidate)
	packageSHA := strings.Repeat("b", 64)
	release := policyregistry.ClassifierReleaseManifestV1{
		SchemaVersion: policyregistry.ServingClassifierReleaseV1,
		APIMajor:      policy.ClassifierAPIMajorV5, WireSchema: policy.SchemaVersionV5,
		Taxonomy: policyregistry.ClassifierTaxonomy{ClassOrder: classOrder, SHA256: policyregistry.TaxonomyDigest(classOrder)},
		Variants: map[policy.ClassifierVariantID]policyregistry.ClassifierVariantManifest{
			policy.ClassifierVariantPrimary: {
				Kind:       policyregistry.ClassifierVariantKindHMMPackage,
				ArtifactID: "hmm_router_sidecar_package_bbbbbbbbbbbbbbbb", ArtifactSHA256: packageSHA,
				PackageWeaveDataRef: &policyregistry.WeaveDataModelArtifactRef{
					DataID: "weave_model_artifact_test", DataKind: policyregistry.ClassifierModelArtifactDataKind,
					SchemaVersion: "hmm_router_classifier_package_v1", Version: "v1",
					StorageURI: "gs://model-artifacts/classifier.tar.gz", ContentSHA256: packageSHA,
					Role: policyregistry.ClassifierModelArtifactRoleTrainedPolicy, Required: true,
				},
				AuxiliaryModels:             map[string]policyregistry.ObjectRef{},
				SemanticConfigurationSHA256: strings.Repeat("a", 64),
				EmbeddingContractSHA256:     strings.Repeat("c", 64),
			},
		},
		RuntimeImageDigest: "sha256:" + strings.Repeat("6", 64),
		Provenance: policyregistry.ClassifierReleaseProvenance{
			RouterRevision: strings.Repeat("7", 40), WeaveRevision: strings.Repeat("8", 40),
			BuildAttestation: artifactRef("build-attestation"),
		},
	}
	releaseRef := store.publishArtifact(t, policyregistry.ServingClassifierReleases, release)
	selection := policyregistry.SelectionSetV4{
		SchemaVersion: policyregistry.ServingSelectionSetV4, Target: policyregistry.TargetInternal,
		Code: policyregistry.CandidateCodeV4{
			Candidate: candidateRef, Project: v3Set.Code.Project, Region: v3Set.Code.Region,
			Router: policyregistry.RevisionBinding{
				Name: "router-worker-internal-v5", URL: "https://router-worker-internal-test.a.run.app",
				Audience: "https://router-worker-internal-test.a.run.app", ImageDigest: candidate.RouterImageDigest,
				Configuration: artifactRef("v5-worker-config"),
			},
			Attestation: artifactRef("binding-attestation"),
		},
		Classifier: policyregistry.ClassifierServiceBinding{
			Release:         releaseRef,
			EnabledVariants: []policy.ClassifierVariantID{policy.ClassifierVariantPrimary},
		},
		DefaultPolicy:    defaultPolicy,
		Profiles:         map[string]policyregistry.PolicyObject{},
		StrategyPolicies: map[router.Strategy]policyregistry.PolicyObject{},
	}
	exported := localV4Release{
		dir: t.TempDir(), store: store, selection: selection, policy: defaultPolicy,
		ref:   store.publishArtifact(t, policyregistry.ServingSelectionSet, selection),
		v3Ref: store.publishArtifact(t, policyregistry.ServingSelectionSet, v3Set),
	}
	for ref, payload := range store.objects {
		writeRegistryObject(t, exported.dir, ref.URI, payload)
	}
	policyRef := policyregistry.ObjectRef{URI: defaultPolicy.URI, SHA256: defaultPolicy.SHA256, Generation: defaultPolicy.Generation}
	policyBytes, err := rosterdata.CanonicalBytes(store.policies[policyRef])
	require.NoError(t, err)
	require.Equal(t, defaultPolicy.SHA256, policyregistry.Digest(policyBytes))
	writeRegistryObject(t, exported.dir, defaultPolicy.URI, policyBytes)
	writeServingRelease(t, exported.dir, exported.ref)
	return exported
}

func writeRegistryObject(t *testing.T, dir, uri string, payload []byte) {
	t.Helper()
	relative, found := strings.CutPrefix(uri, testRegistryRoot+"/")
	require.True(t, found, uri)
	path := filepath.Join(dir, filepath.FromSlash(relative))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, payload, 0o644))
}

func writeServingRelease(t *testing.T, dir string, selection policyregistry.ObjectRef) {
	t.Helper()
	payload, err := json.Marshal(policyregistry.LocalServingRelease{
		SchemaVersion: policyregistry.LocalServingReleaseSchemaV1, RootURI: testRegistryRoot,
		Target: policyregistry.TargetInternal, SelectionSet: selection,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "serving_release.json"), payload, 0o644))
}

func TestServingDirectoryPreparesTheExportedV4SelectionOnTheSelfHostedWorker(t *testing.T) {
	local := writeLocalV4Release(t)
	directory, err := policyregistry.NewServingDirectory(local.dir, selfHostedWorkerURL+"/")
	require.NoError(t, err)
	view := local.selection.View(local.ref)
	prepared, err := policyregistry.ReadPreparedSelection(context.Background(), directory, policyregistry.TargetInternal, "", view.Default)
	require.NoError(t, err)
	require.NotNil(t, prepared.ClassifierReleaseV5)
	require.Equal(t, selfHostedWorkerURL, prepared.ClassifierRuntimeBinding.Binding.WorkerURL)
	require.Equal(t, selfHostedWorkerURL, prepared.ClassifierRuntimeBinding.Binding.WorkerAudience)
	require.Equal(t, prepared.ClassifierReleaseV5.RuntimeImageDigest, prepared.ClassifierRuntimeBinding.Binding.ImageDigest)
	require.Equal(t, local.policy.SHA256, prepared.Policy.SHA256)
	_, err = directory.ReadServingState(context.Background(), policyregistry.TargetInternal)
	require.Error(t, err, "a local release has no admission state")
}

func TestManagedRuntimeBindingsStillRequireTheCloudRunClassifierWorker(t *testing.T) {
	releaseSHA := strings.Repeat("3", 64)
	for _, origin := range []string{selfHostedWorkerURL, "https://classifier-worker", "https://router-classifier-worker.example.com", "http://router-classifier-worker-abc-uc.a.run.app"} {
		binding := testRuntimeBinding(policyregistry.EnvironmentProd, releaseSHA)
		binding["worker_url"], binding["worker_audience"] = origin, origin
		_, err := policyregistry.DecodeClassifierRuntimeBinding(encodeRuntimeBinding(t, binding), policyregistry.EnvironmentProd, releaseSHA)
		require.ErrorContains(t, err, "canonical Cloud Run origins", origin)
	}
}

func TestServingDirectoryFailsClosed(t *testing.T) {
	local := writeLocalV4Release(t)
	for _, origin := range []string{"", "classifier-worker:8093", "ftp://classifier-worker", "http://user@classifier-worker", "http://classifier-worker/v5", "http://classifier-worker?x=1"} {
		_, err := policyregistry.NewServingDirectory(local.dir, origin)
		require.ErrorContains(t, err, "invalid self-hosted classifier worker origin", origin)
	}

	writeServingRelease(t, local.dir, local.v3Ref)
	_, err := policyregistry.NewServingDirectory(local.dir, selfHostedWorkerURL)
	require.ErrorContains(t, err, "V4 selection set")

	writeServingRelease(t, local.dir, local.ref)
	writeRegistryObject(t, local.dir, local.ref.URI, append(slices.Clone(local.store.objects[local.ref]), ' '))
	_, err = policyregistry.NewServingDirectory(local.dir, selfHostedWorkerURL)
	require.ErrorContains(t, err, "does not match its digest")
}

func activateLocalSelection(local localV4Release, selection policyregistry.ObjectRef) {
	local.store.states[policyregistry.TargetInternal] = policyregistry.ServingStateSnapshot{
		State: policyregistry.ServingControlState{
			Target: policyregistry.TargetInternal, CurrentActivationID: "activation-1",
			Activations: map[string]policyregistry.Activation{"activation-1": {ID: "activation-1", SelectionSet: selection}},
		},
		Generation: 1,
	}
}

func TestExportServingTargetV4WritesTheExactClosureTheSelfHostedRouterServes(t *testing.T) {
	local := writeLocalV4Release(t)
	activateLocalSelection(local, local.ref)
	local.store.putRuntimeBinding(t, policyregistry.EnvironmentProd, local.selection.Classifier.Release.SHA256, testRuntimeBinding(policyregistry.EnvironmentProd, local.selection.Classifier.Release.SHA256))
	dir := t.TempDir()
	exported, err := policyregistry.ExportServingTargetV4(context.Background(), local.store, policyregistry.TargetInternal, dir)
	require.NoError(t, err)
	require.Equal(t, local.ref, exported.Release.SelectionSet)
	require.Equal(t, local.selection.Classifier.Release, exported.ClassifierRelease)
	require.Equal(t, "activation-1", exported.ActivationID)
	relative, _ := strings.CutPrefix(local.ref.URI, testRegistryRoot+"/")
	written, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(relative)))
	require.NoError(t, err)
	require.Equal(t, local.store.objects[local.ref], written, "the selection set is exported byte for byte")
	_, err = os.Stat(filepath.Join(dir, "runtime_bindings"))
	require.True(t, os.IsNotExist(err), "the managed runtime binding is never exported")

	directory, err := policyregistry.NewServingDirectory(dir, selfHostedWorkerURL)
	require.NoError(t, err)
	prepared, err := policyregistry.ReadPreparedSelection(context.Background(), directory, policyregistry.TargetInternal, "", local.selection.View(local.ref).Default)
	require.NoError(t, err)
	require.Equal(t, selfHostedWorkerURL, prepared.ClassifierRuntimeBinding.Binding.WorkerURL)
}

func TestExportServingTargetV4FailsClosed(t *testing.T) {
	local := writeLocalV4Release(t)
	_, err := policyregistry.ExportServingTargetV4(context.Background(), local.store, policyregistry.TargetInternal, t.TempDir())
	require.Error(t, err, "a target without an active selection has nothing to export")

	activateLocalSelection(local, local.ref)
	_, err = policyregistry.ExportServingTargetV4(context.Background(), local.store, policyregistry.TargetInternal, t.TempDir())
	require.Error(t, err, "a release without a managed runtime binding is not servable")

	activateLocalSelection(local, local.v3Ref)
	_, err = policyregistry.ExportServingTargetV4(context.Background(), local.store, policyregistry.TargetInternal, t.TempDir())
	require.ErrorContains(t, err, "read V4 selection set for serving target")
}

func TestExportServingTargetV4ExportsEveryProfileLane(t *testing.T) {
	local := writeLocalV4Release(t)
	cohort := local.store.object(t, policyregistry.ServingSelectionSet, local.v3Ref).(*policyregistry.SelectionSetV3)
	require.NotEmpty(t, cohort.Profiles)
	selection := local.selection
	selection.Profiles = maps.Clone(cohort.Profiles)
	ref := local.store.publishArtifact(t, policyregistry.ServingSelectionSet, selection)
	activateLocalSelection(local, ref)
	local.store.putRuntimeBinding(t, policyregistry.EnvironmentProd, selection.Classifier.Release.SHA256, testRuntimeBinding(policyregistry.EnvironmentProd, selection.Classifier.Release.SHA256))
	dir := t.TempDir()

	exported, err := policyregistry.ExportServingTargetV4(context.Background(), local.store, policyregistry.TargetInternal, dir)
	require.NoError(t, err)
	require.Equal(t, ref, exported.Release.SelectionSet)

	for _, policyObject := range selection.Profiles {
		relative, _ := strings.CutPrefix(policyObject.URI, testRegistryRoot+"/")
		written, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(relative)))
		require.NoError(t, err)
		require.Equal(t, policyObject.SHA256, policyregistry.Digest(written))
	}
	directory, err := policyregistry.NewServingDirectory(dir, selfHostedWorkerURL)
	require.NoError(t, err)
	view := selection.View(ref)
	for profileKey, lane := range view.Profiles {
		prepared, err := policyregistry.ReadPreparedSelection(context.Background(), directory, policyregistry.TargetInternal, profileKey, lane)
		require.NoError(t, err, profileKey)
		require.Equal(t, selection.Profiles[profileKey].SHA256, prepared.Candidate.Policy.SHA256, profileKey)
	}
}
