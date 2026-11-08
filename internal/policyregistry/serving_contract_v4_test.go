package policyregistry

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

const classifierContractRegistryRoot = "gs://classifier-contract-test/weave_registry"

func classifierContractArtifactRef(label string) ObjectRef {
	digest := Digest([]byte(label))
	return ObjectRef{
		URI:        classifierContractRegistryRoot + "/artifacts/" + digest + ".json",
		SHA256:     digest,
		Generation: 1,
	}
}

func classifierContractPolicy() PolicyObject {
	sha := strings.Repeat("a", 64)
	return PolicyObject{
		URI:           classifierContractRegistryRoot + "/router_policy/v1/policies/sha256/" + sha + ".json",
		SHA256:        sha,
		SchemaVersion: rosterdata.SchemaVersionPolicyV1,
		Generation:    1,
	}
}

func classifierContractRelease() ClassifierReleaseManifestV1 {
	classOrder := []string{"fast", "maximum"}
	packageSHA := strings.Repeat("b", 64)
	return ClassifierReleaseManifestV1{
		SchemaVersion: ServingClassifierReleaseV1,
		APIMajor:      policy.ClassifierAPIMajorV5,
		WireSchema:    policy.SchemaVersionV5,
		Taxonomy:      ClassifierTaxonomy{ClassOrder: classOrder, SHA256: TaxonomyDigest(classOrder)},
		Variants: map[policy.ClassifierVariantID]ClassifierVariantManifest{
			policy.ClassifierVariantPrimary: {
				Kind: ClassifierVariantKindHMMPackage, ArtifactID: "hmm-primary", ArtifactSHA256: packageSHA,
				PackageWeaveDataRef: &WeaveDataModelArtifactRef{
					DataID: "data-123", DataKind: ClassifierModelArtifactDataKind,
					SchemaVersion: "hmm_router_classifier_package_v1", Version: "v1",
					StorageURI: "gs://model-artifacts/package.tgz", ContentSHA256: packageSHA,
					Role: ClassifierModelArtifactRoleTrainedPolicy, Required: true,
				},
				AuxiliaryModels: map[string]ObjectRef{}, SemanticConfigurationSHA256: strings.Repeat("c", 64),
				EmbeddingContractSHA256: strings.Repeat("d", 64),
			},
			policy.ClassifierVariantQwen: {
				Kind: ClassifierVariantKindQwenBundle, ArtifactID: "qwen-classifier",
				ArtifactSHA256: strings.Repeat("e", 64), BundleSHA256: strings.Repeat("e", 64),
				AuxiliaryModels: map[string]ObjectRef{}, SemanticConfigurationSHA256: strings.Repeat("f", 64),
			},
		},
		RuntimeImageDigest: "sha256:" + strings.Repeat("1", 64),
		Provenance: ClassifierReleaseProvenance{
			RouterRevision: strings.Repeat("2", 40), WeaveRevision: strings.Repeat("3", 40),
			BuildAttestation: classifierContractArtifactRef("classifier-build"),
		},
	}
}

func TestClassifierReleaseV1SupportsWeaveDataAndQwenBundleSources(t *testing.T) {
	release := classifierContractRelease()
	payload, err := CanonicalBytes(release)
	require.NoError(t, err)
	manifest, err := DecodePublishableServingManifest(payload, classifierContractRegistryRoot, ServingClassifierReleases)
	require.NoError(t, err)
	require.Equal(t, &release, manifest)

	wrongPackageDigest := release
	wrongVariant := wrongPackageDigest.Variants[policy.ClassifierVariantPrimary]
	wrongVariant.PackageWeaveDataRef = &WeaveDataModelArtifactRef{
		DataID: "data-123", DataKind: ClassifierModelArtifactDataKind,
		SchemaVersion: "hmm_router_classifier_package_v1", Version: "v1",
		StorageURI: "gs://model-artifacts/package.tgz", ContentSHA256: strings.Repeat("9", 64),
		Role: ClassifierModelArtifactRoleTrainedPolicy, Required: true,
	}
	wrongPackageDigest.Variants[policy.ClassifierVariantPrimary] = wrongVariant
	require.ErrorContains(t, wrongPackageDigest.Validate(classifierContractRegistryRoot), "does not match artifact_sha256")

	fakeQwenRef := classifierContractRelease()
	qwenVariant := fakeQwenRef.Variants[policy.ClassifierVariantQwen]
	qwenVariant.PackageWeaveDataRef = wrongVariant.PackageWeaveDataRef
	fakeQwenRef.Variants[policy.ClassifierVariantQwen] = qwenVariant
	require.ErrorContains(t, fakeQwenRef.Validate(classifierContractRegistryRoot), "Qwen variant")

	unsupportedAuxiliaryRelease := classifierContractRelease()
	primary := unsupportedAuxiliaryRelease.Variants[policy.ClassifierVariantPrimary]
	primary.AuxiliaryModels = map[string]ObjectRef{"classifier": classifierContractArtifactRef("auxiliary-classifier")}
	unsupportedAuxiliaryRelease.Variants[policy.ClassifierVariantPrimary] = primary
	require.ErrorContains(t, unsupportedAuxiliaryRelease.Validate(classifierContractRegistryRoot), "unsupported auxiliary model refs")
}

func TestClassifierReleaseRejectsDuplicateLabelsAndMissingStorage(t *testing.T) {
	duplicateLabels := classifierContractRelease()
	duplicateLabels.Taxonomy.ClassOrder = append(slices.Clone(duplicateLabels.Taxonomy.ClassOrder), duplicateLabels.Taxonomy.ClassOrder[0])
	duplicateLabels.Taxonomy.SHA256 = TaxonomyDigest(duplicateLabels.Taxonomy.ClassOrder)
	require.ErrorContains(t, duplicateLabels.Validate(classifierContractRegistryRoot), "taxonomy is invalid")

	missingStorage := classifierContractRelease()
	primary := missingStorage.Variants[policy.ClassifierVariantPrimary]
	packageRef := *primary.PackageWeaveDataRef
	packageRef.StorageURI = " "
	primary.PackageWeaveDataRef = &packageRef
	missingStorage.Variants[policy.ClassifierVariantPrimary] = primary
	require.ErrorContains(t, missingStorage.Validate(classifierContractRegistryRoot), "WeaveDataRef is incomplete")
}

func TestClassifierTaxonomyDigestUsesGoJSONHTMLEscaping(t *testing.T) {
	classOrder := []string{"<&>\u2028\u2029"}
	require.Equal(t, "bf1f62894c7640c81ac0fdaa084c86a1e39d5886f11df541f8617cca246b5058", TaxonomyDigest(classOrder))
}

func TestCandidateV3AndSelectionSetV4KeepPhysicalClassifierOutOfTargetSet(t *testing.T) {
	policyObject := classifierContractPolicy()
	candidate := CandidateV3{
		SchemaVersion: ServingCandidateV3, RouterImageDigest: "sha256:" + strings.Repeat("1", 64),
		Requirements: CandidateRequirementsV3{
			RuntimeContract: ManagedRuntimeContractV1, PolicySchema: rosterdata.SchemaVersionPolicyV1,
			SupportedAPIMajors: []policy.ClassifierAPIMajor{policy.ClassifierAPIMajorV4, policy.ClassifierAPIMajorV5},
		},
		Provenance: ServingProvenance{
			RouterRevision: strings.Repeat("2", 40), WeaveRevision: strings.Repeat("3", 40),
			BuildAttestation: classifierContractArtifactRef("router-build"),
		},
	}
	candidateRef := objectReferenceForTest(t, ServingCandidate, candidate)
	release := classifierContractRelease()
	releaseRef := objectReferenceForTest(t, ServingClassifierReleases, release)
	classifierBinding := ClassifierServiceBinding{
		Release: releaseRef, ServiceURL: "https://classifier-gateway.example",
		Audience:        "https://classifier-gateway.example",
		EnabledVariants: []policy.ClassifierVariantID{policy.ClassifierVariantPrimary},
	}
	selectionSet := SelectionSetV4{
		SchemaVersion: ServingSelectionSetV4, Target: TargetStable,
		Code: CandidateCodeV4{
			Candidate: candidateRef, Project: "prod-01", Region: "us-central1",
			Router: RevisionBinding{
				Name: "router-0001", URL: "https://router-0001.example", Audience: "https://router.example",
				ImageDigest: candidate.RouterImageDigest, Configuration: classifierContractArtifactRef("router-config"),
			},
			Attestation: classifierContractArtifactRef("router-attestation"),
		},
		Classifier: classifierBinding, DefaultPolicy: policyObject,
		StrategyPolicies: map[router.Strategy]PolicyObject{}, Profiles: map[string]PolicyObject{},
	}
	require.NoError(t, candidate.Validate(classifierContractRegistryRoot))
	require.NoError(t, selectionSet.Validate(classifierContractRegistryRoot))

	selectionSet.Classifier.ServiceURL, selectionSet.Classifier.Audience = "", ""
	require.NoError(t, selectionSet.Validate(classifierContractRegistryRoot), "a gateway-free selection defers dispatch to the runtime binding")
	selectionSet.Classifier.ServiceURL = "http://classifier-gateway.example"
	require.ErrorContains(t, selectionSet.Validate(classifierContractRegistryRoot), "stable HTTPS service origin", "historical gateway fields stay validated when present")
	selectionSet.Classifier = classifierBinding

	selectionSet.Classifier.EnabledVariants = []policy.ClassifierVariantID{
		policy.ClassifierVariantPrimary, policy.ClassifierVariantQwen,
	}
	require.ErrorContains(t, selectionSet.Validate(classifierContractRegistryRoot), "internal-only variant")
	selectionSet.Classifier.EnabledVariants = []policy.ClassifierVariantID{policy.ClassifierVariantPrimary}

	selectionSetRef := objectReferenceForTest(t, ServingSelectionSet, selectionSet)
	sessionSelection := selectionSet.View(selectionSetRef).Default
	require.Equal(t, candidateRef, sessionSelection.Release)
	require.Equal(t, selectionSetRef, sessionSelection.Binding)

	selectionSet.Profiles = nil
	require.ErrorContains(t, selectionSet.Validate(classifierContractRegistryRoot), "explicit policy maps")
	selectionSet.Profiles = map[string]PolicyObject{}
	selectionSet.Classifier.EnabledVariants = []policy.ClassifierVariantID{policy.ClassifierVariantQwen, policy.ClassifierVariantPrimary}
	require.ErrorContains(t, selectionSet.Validate(classifierContractRegistryRoot), "sorted and unique")
}

func objectReferenceForTest(t *testing.T, kind ServingKind, manifest ServingManifest) ObjectRef {
	t.Helper()
	payload, err := CanonicalBytes(manifest)
	require.NoError(t, err)
	_, err = DecodePublishableServingManifest(payload, classifierContractRegistryRoot, kind)
	require.NoError(t, err)
	sha := Digest(payload)
	return ObjectRef{
		URI:    classifierContractRegistryRoot + "/artifacts/" + sha + ".json",
		SHA256: sha, Generation: 1,
	}
}
