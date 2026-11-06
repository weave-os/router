package policyregistry_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

const alternateRosterArm = "openai/gpt-6-sol"

func publishRosterArm(t *testing.T, store *servingMemoryStore, base policyregistry.PolicyObject, arm string) policyregistry.PolicyObject {
	t.Helper()
	roster := store.policies[policyregistry.ObjectRef{URI: base.URI, SHA256: base.SHA256, Generation: base.Generation}]
	payload, err := rosterdata.CanonicalBytes(roster)
	require.NoError(t, err)
	payload = bytes.ReplaceAll(payload, []byte(roster.AllArms()[0]), []byte(arm))
	changed, err := rosterdata.ParseValidated(payload)
	require.NoError(t, err)
	digest := policyregistry.Digest(payload)
	ref := policyregistry.ObjectRef{URI: testRegistryRoot + "/router_policy/v1/policies/sha256/" + digest + ".json", SHA256: digest, Generation: 1}
	store.policies[ref] = changed
	return policyregistry.PolicyObject{URI: ref.URI, SHA256: ref.SHA256, Generation: ref.Generation, SchemaVersion: changed.SchemaVersion}
}

func cohortFixture(t *testing.T) (*servingMemoryStore, policyregistry.SelectionSetV3) {
	t.Helper()
	store, _, legacy := controllerFixture(t)
	lane := foldLane(t, store, legacy.Default)
	candidate := store.object(t, policyregistry.ServingCandidate, lane.Candidate).(*policyregistry.CandidateV2)
	set := policyregistry.SelectionSetV3{SchemaVersion: policyregistry.ServingSelectionSetV3, Target: legacy.Target,
		Code: policyregistry.CodeBinding{Candidate: lane.Candidate, LaneBinding: lane.LaneBinding}, DefaultPolicy: candidate.Policy,
		Profiles: map[string]policyregistry.PolicyObject{profileKeyOne: candidate.Policy, profileKeyTwo: publishRosterArm(t, store, candidate.Policy, alternateRosterArm)}}
	return store, set
}

func cohortProposal(t *testing.T, store *servingMemoryStore, set policyregistry.SelectionSetV3, previous *policyregistry.ObjectRef, scope policyregistry.ChangeScope, profile string) policyregistry.DeploymentProposalV2 {
	t.Helper()
	return policyregistry.DeploymentProposalV2{SchemaVersion: policyregistry.ServingProposalV2, Target: set.Target, PreviousSelectionSet: previous,
		SelectionSet: store.publishArtifact(t, policyregistry.ServingSelectionSet, set), SourceCandidate: set.Code.Candidate, Scope: scope, ProfileKey: profile,
		Actor: "test-operator", Reason: "cohort configuration", RequestID: "synthetic-cohort-release", CreatedAt: servingEpoch,
		Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{}}
}

func TestSelectionSetV3StrictContractAndHistoricalDecoders(t *testing.T) {
	store, set := cohortFixture(t)
	payload := servingPayload(t, set)
	decoded, err := policyregistry.DecodePublishableServingManifest(payload, testRegistryRoot, policyregistry.ServingSelectionSet)
	require.NoError(t, err)
	require.Equal(t, &set, decoded)
	for name, mutate := range map[string]func(*policyregistry.SelectionSetV3){
		"missing profiles": func(s *policyregistry.SelectionSetV3) { s.Profiles = nil },
		"mutable policy":   func(s *policyregistry.SelectionSetV3) { s.DefaultPolicy.Generation = 0 },
		"foreign policy": func(s *policyregistry.SelectionSetV3) {
			s.Profiles[profileKeyOne] = policyregistry.PolicyObject{URI: "gs://foreign/policy"}
		},
		"profile key":     func(s *policyregistry.SelectionSetV3) { s.Profiles["untrusted-key"] = s.DefaultPolicy },
		"code generation": func(s *policyregistry.SelectionSetV3) { s.Code.Candidate.Generation = 0 },
		"target":          func(s *policyregistry.SelectionSetV3) { s.Target = "prod/customer" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := set
			invalid.Profiles = maps.Clone(set.Profiles)
			mutate(&invalid)
			require.Error(t, invalid.Validate(testRegistryRoot))
		})
	}
	for _, invalid := range [][]byte{
		bytes.Replace(payload, []byte(`"code":{`), []byte(`"code":{"profile_key":"forbidden",`), 1),
		bytes.Replace(payload, []byte(`"default_policy":{`), []byte(`"default_policy":{"candidate":{},`), 1),
		append(bytes.Clone(payload), []byte(`{}`)...),
		bytes.Replace(payload, []byte(policyregistry.ServingSelectionSetV3), []byte(policyregistry.ServingSelectionSetV2), 1),
	} {
		_, err := policyregistry.DecodePublishableServingManifest(invalid, testRegistryRoot, policyregistry.ServingSelectionSet)
		require.Error(t, err)
	}
	ref := store.publishArtifact(t, policyregistry.ServingSelectionSet, set)
	for key, selection := range set.View(ref).Profiles {
		prepared, err := policyregistry.ReadPreparedSelection(context.Background(), store, set.Target, key, selection)
		require.NoError(t, err)
		require.Equal(t, set.Profiles[key], prepared.PolicyReference)
		require.Equal(t, set.DefaultPolicy, prepared.Candidate.Policy, "candidate remains its immutable build composition")
		require.Equal(t, set.Code.LaneBinding, prepared.Binding)
	}
	_, err = policyregistry.DecodeServingObject(payload, testRegistryRoot, policyregistry.ServingSelectionSet, namespaceRef(policyregistry.ServingSelectionSets, "v3-in-v1"))
	require.ErrorContains(t, err, "storage layout")
}

func TestSelectionSetV3RejectsUnavailableOrIncompatiblePolicy(t *testing.T) {
	for _, failure := range []string{"missing", "taxonomy", "schema", "catalog"} {
		t.Run(failure, func(t *testing.T) {
			store, set := cohortFixture(t)
			policy := set.Profiles[profileKeyTwo]
			ref := policyregistry.ObjectRef{URI: policy.URI, SHA256: policy.SHA256, Generation: policy.Generation}
			switch failure {
			case "missing":
				delete(store.policies, ref)
			case "taxonomy":
				store.policies[ref].ClassOrder = []string{"other"}
			case "schema":
				store.policies[ref].SchemaVersion = rosterdata.SchemaVersion("unsupported")
			case "catalog":
				store.policies[ref].Clusters["low"].Arms[0] = "unknown/unavailable"
			}
			setRef := store.publishArtifact(t, policyregistry.ServingSelectionSet, set)
			cache := cohortCache(t, store)
			_, err := cache.Snapshot(context.Background(), policyregistry.SessionReleaseBinding{Target: set.Target, ProfileKey: profileKeyTwo, Selection: set.View(setRef).Profiles[profileKeyTwo]})
			require.Error(t, err, "invalid assigned policy must not fall back to default or a cached customer")
		})
	}
}

func TestSelectionSetV3TransitionPreservesV2ProfilePolicies(t *testing.T) {
	store, controller, legacy, initial := heterogeneousLaneFixture(t)
	v2 := store.object(t, policyregistry.ServingSelectionSet, initial.Activation.SelectionSet).(*policyregistry.SelectionSetV2)
	candidate := store.object(t, policyregistry.ServingCandidate, v2.Default.Candidate).(*policyregistry.CandidateV2)
	set := policyregistry.SelectionSetV3{SchemaVersion: policyregistry.ServingSelectionSetV3, Target: legacy.Target,
		Code: policyregistry.CodeBinding{Candidate: v2.Default.Candidate, LaneBinding: v2.Default.LaneBinding}, DefaultPolicy: candidate.Policy, Profiles: map[string]policyregistry.PolicyObject{}}
	for key, lane := range v2.Profiles {
		set.Profiles[key] = *lane.ProfilePolicy
	}
	proposal := cohortProposal(t, store, set, &initial.Activation.SelectionSet, policyregistry.ChangeFull, "")
	require.NoError(t, controller.ValidateProposal(context.Background(), proposal))
	for key, old := range v2.View(initial.Activation.SelectionSet).Profiles {
		oldPrepared, err := policyregistry.ReadPreparedSelection(context.Background(), store, set.Target, key, old)
		require.NoError(t, err)
		nextPrepared, err := policyregistry.ReadPreparedSelection(context.Background(), store, set.Target, key, set.View(proposal.SelectionSet).Profiles[key])
		require.NoError(t, err)
		require.Equal(t, oldPrepared.PolicyReference, nextPrepared.PolicyReference)
		require.Equal(t, oldPrepared.Policy, nextPrepared.Policy)
	}
	set.Profiles[profileKeyTwo] = set.DefaultPolicy
	proposal.SelectionSet = store.publishArtifact(t, policyregistry.ServingSelectionSet, set)
	require.ErrorContains(t, controller.ValidateProposal(context.Background(), proposal), "retain destination profile revisions")
}

func TestControllerAtomicallyTransitionsV3CandidateV2ToV4CandidateV3AndRetainsOldPin(t *testing.T) {
	ctx := context.Background()
	store, v3Set := cohortFixture(t)
	v3Set.Target = policyregistry.TargetInternal
	legacyCandidate := *store.object(t, policyregistry.ServingCandidate, v3Set.Code.Candidate).(*policyregistry.CandidateV2)
	qwenPolicy := publishRosterArm(t, store, legacyCandidate.Policy, alternateRosterArm)
	legacyQwen := policy.ClassifierArtifact{
		ArtifactID:      "hmm_router_qwen_turn_classifier_3a14c1f5a005c039",
		PackageSHA256:   strings.Repeat("d", 64),
		Endpoint:        "https://qwen-test.modal.run/",
		Audience:        "https://weave-router/qwen-classifier",
		ServingRevision: strings.Repeat("e", 64),
	}
	legacyCandidate.StrategyPolicies = map[router.Strategy]policyregistry.PolicyObject{
		router.StrategyHMMQwen: qwenPolicy,
	}
	legacyCandidate.Classifier.Identity.Variants = map[router.Strategy]policy.ClassifierArtifact{
		router.StrategyHMMQwen: legacyQwen,
	}
	legacyCandidateRef := store.publishArtifact(t, policyregistry.ServingCandidate, legacyCandidate)
	v3Set.Code.Candidate = legacyCandidateRef
	v3Ref := store.publishArtifact(t, policyregistry.ServingSelectionSet, v3Set)
	controller, err := policyregistry.NewServingController(
		store,
		preparedValidator(func(context.Context, policyregistry.PreparedSelection) error { return nil }),
		func() time.Time { return servingEpoch.Add(2 * time.Second) },
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	require.NoError(t, err)
	activateV3 := policyregistry.DeploymentProposalV2{
		SchemaVersion: policyregistry.ServingProposalV2, Target: v3Set.Target,
		SelectionSet: v3Ref, SourceCandidate: legacyCandidateRef, Scope: policyregistry.ChangeFull,
		Actor: "test-operator", Reason: "seed v3 before atomic classifier migration",
		RequestID: "seed-v3-selection", CreatedAt: servingEpoch,
		Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{},
	}
	activatedV3, err := controller.Activate(ctx, store.publishArtifact(t, policyregistry.ServingProposal, activateV3), "test-operator")
	require.NoError(t, err)

	classOrder := slices.Clone(legacyCandidate.Classifier.Identity.ClassOrder)
	packageSHA := strings.Repeat("b", 64)
	classer := policyregistry.CandidateV3{
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
	candidateV3Ref := store.publishArtifact(t, policyregistry.ServingCandidate, classer)
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
			policy.ClassifierVariantQwen: {
				Kind:           policyregistry.ClassifierVariantKindQwenBundle,
				ArtifactID:     legacyQwen.ArtifactID,
				ArtifactSHA256: legacyQwen.PackageSHA256, BundleSHA256: legacyQwen.PackageSHA256,
				AuxiliaryModels:             map[string]policyregistry.ObjectRef{},
				SemanticConfigurationSHA256: strings.Repeat("f", 64),
			},
		},
		RuntimeImageDigest: "sha256:" + strings.Repeat("4", 64),
		Provenance: policyregistry.ClassifierReleaseProvenance{
			RouterRevision: strings.Repeat("7", 40), WeaveRevision: strings.Repeat("8", 40),
			BuildAttestation: artifactRef("build-attestation"),
		},
	}
	releaseRef := store.publishArtifact(t, policyregistry.ServingClassifierReleases, release)
	selectionV4 := policyregistry.SelectionSetV4{
		SchemaVersion: policyregistry.ServingSelectionSetV4, Target: v3Set.Target,
		Code: policyregistry.CandidateCodeV4{
			Candidate: candidateV3Ref, Project: v3Set.Code.Project, Region: v3Set.Code.Region,
			Router: policyregistry.RevisionBinding{
				Name: "router-worker-internal-v5", URL: "https://router-worker-internal-test.a.run.app",
				Audience: "https://router-worker-internal-test.a.run.app", ImageDigest: classer.RouterImageDigest,
				Configuration: artifactRef("v5-worker-config"),
			},
			Attestation: artifactRef("binding-attestation"),
		},
		Classifier: policyregistry.ClassifierServiceBinding{
			Release:         releaseRef,
			ServiceURL:      "https://router-classifier-gateway-test.a.run.app",
			Audience:        "https://router-classifier-gateway-test.a.run.app",
			EnabledVariants: []policy.ClassifierVariantID{policy.ClassifierVariantPrimary, policy.ClassifierVariantQwen},
		},
		DefaultPolicy: v3Set.DefaultPolicy,
		Profiles:      maps.Clone(v3Set.Profiles),
		StrategyPolicies: map[router.Strategy]policyregistry.PolicyObject{
			router.StrategyHMMQwen: qwenPolicy,
		},
	}
	v4Ref := store.publishArtifact(t, policyregistry.ServingSelectionSet, selectionV4)
	activateV4 := policyregistry.DeploymentProposalV2{
		SchemaVersion: policyregistry.ServingProposalV2, Target: v3Set.Target,
		PreviousSelectionSet: &activatedV3.Activation.SelectionSet,
		SelectionSet:         v4Ref, SourceCandidate: candidateV3Ref, Scope: policyregistry.ChangeFull,
		Actor: "test-operator", Reason: "atomically migrate to classifier service v5",
		RequestID: "migrate-v3-to-v4", CreatedAt: servingEpoch.Add(time.Second),
		Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{},
	}
	require.NoError(t, controller.ValidateProposal(ctx, activateV4))
	mutatedProfileSet := selectionV4
	mutatedProfileSet.Profiles = maps.Clone(selectionV4.Profiles)
	mutatedProfileSet.Profiles[profileKeyTwo] = selectionV4.DefaultPolicy
	mutatedProposal := activateV4
	mutatedProposal.SelectionSet = store.publishArtifact(t, policyregistry.ServingSelectionSet, mutatedProfileSet)
	mutatedProposal.RequestID = "migrate-v3-to-v4-mutated-profile"
	require.ErrorContains(t, controller.ValidateProposal(ctx, mutatedProposal), "retain destination profile revisions")
	activatedV4, err := controller.Activate(ctx, store.publishArtifact(t, policyregistry.ServingProposal, activateV4), "test-operator")
	require.NoError(t, err)
	require.Equal(t, activatedV3.Snapshot.Generation+1, activatedV4.Snapshot.Generation)

	oldSelection := v3Set.View(v3Ref).Default
	oldPrepared, err := policyregistry.ReadPreparedSelection(ctx, store, v3Set.Target, "", oldSelection)
	require.NoError(t, err, "retained session pins still decode the historical CandidateV2/V3 set")
	require.Equal(t, legacyQwen.PackageSHA256, oldPrepared.Candidate.Classifier.Identity.Variants[router.StrategyHMMQwen].PackageSHA256)

	newSelection := selectionV4.View(v4Ref).Default
	newPrepared, err := policyregistry.ReadPreparedSelection(ctx, store, v3Set.Target, "", newSelection)
	require.NoError(t, err)
	require.Equal(t, artifactRef("build-attestation"), newPrepared.Candidate.ClassifierReleaseV5.Provenance.BuildAttestation)
	require.Equal(t, releaseRef.SHA256, newPrepared.ClassifierServiceBinding.Release.SHA256)
	qwenVariant := newPrepared.ClassifierReleaseV5.Variants[policy.ClassifierVariantQwen]
	require.Equal(t, legacyQwen.ArtifactID, qwenVariant.ArtifactID)
	require.Equal(t, legacyQwen.PackageSHA256, qwenVariant.BundleSHA256)
	require.Equal(t, qwenPolicy.SHA256, newPrepared.StrategyPolicies[router.StrategyHMMQwen].Reference.SHA256)
	require.Equal(t, v3Set.Profiles, selectionV4.Profiles, "V3 policy profiles remain assigned during the V4 migration")
}
