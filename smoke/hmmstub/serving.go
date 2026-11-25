package hmmstub

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/rosterdata"
)

const (
	ServingTarget             = policyregistry.TargetStable
	ServingProject            = "local"
	ServingRegion             = "local"
	ServingRevision           = "hmm-stub-router"
	ServingImageDigest        = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	ServingSelectionSetSHA256 = "50909073bddd4c1412b5d1cf00a7f57708ab0591d1d65bb8f94e19320862d227"
	servingActivationID       = "00000000-0000-4000-8000-000000000001"
	servingRouterOrigin       = "https://router.localhost"
	servingSourceRevision     = "0000000000000000000000000000000000000000"
)

var servingCreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func artifactRef(label string) policyregistry.ObjectRef {
	digest := policyregistry.Digest([]byte(label))
	return policyregistry.ObjectRef{URI: registryRoot + "/artifacts/" + digest + ".json", SHA256: digest, Generation: 1}
}

func ServingConfiguration() policyregistry.ObjectRef {
	return artifactRef("hmm-stub-router-configuration")
}

func classifierRelease() policyregistry.ClassifierReleaseManifestV1 {
	identity := classifier()
	return policyregistry.ClassifierReleaseManifestV1{
		SchemaVersion: policyregistry.ServingClassifierReleaseV1,
		APIMajor:      policy.ClassifierAPIMajorV5,
		WireSchema:    policy.SchemaVersionV5,
		Taxonomy:      policyregistry.ClassifierTaxonomy{ClassOrder: identity.ClassOrder, SHA256: identity.TaxonomySHA256},
		Variants: map[policy.ClassifierVariantID]policyregistry.ClassifierVariantManifest{
			policy.ClassifierVariantPrimary: {
				Kind: policyregistry.ClassifierVariantKindHMMPackage, ArtifactID: identity.ArtifactID, ArtifactSHA256: identity.PackageSHA256,
				PackageWeaveDataRef: &policyregistry.WeaveDataModelArtifactRef{
					DataID: "hmm-stub", DataKind: policyregistry.ClassifierModelArtifactDataKind,
					SchemaVersion: "hmm_router_classifier_package_v1", Version: "v1",
					StorageURI: registryRoot + "/packages/hmm-stub.tgz", ContentSHA256: identity.PackageSHA256,
					Role: policyregistry.ClassifierModelArtifactRoleTrainedPolicy, Required: true,
				},
				AuxiliaryModels:             map[string]policyregistry.ObjectRef{},
				SemanticConfigurationSHA256: strings.Repeat("d", 64),
				EmbeddingContractSHA256:     strings.Repeat("e", 64),
			},
		},
		RuntimeImageDigest: identity.ImageDigest,
		Provenance: policyregistry.ClassifierReleaseProvenance{
			RouterRevision: servingSourceRevision, WeaveRevision: servingSourceRevision,
			BuildAttestation: artifactRef("hmm-stub-classifier-build"),
		},
	}
}

type servingObject struct {
	relative string
	payload  []byte
	ref      policyregistry.ObjectRef
}

func canonicalArtifact(manifest any) (servingObject, error) {
	payload, err := policyregistry.CanonicalBytes(manifest)
	if err != nil {
		return servingObject{}, err
	}
	digest := policyregistry.Digest(payload)
	relative := "artifacts/" + digest + ".json"
	return servingObject{relative: relative, payload: payload, ref: policyregistry.ObjectRef{URI: registryRoot + "/" + relative, SHA256: digest, Generation: 1}}, nil
}

type ServingGraph struct {
	SelectionSet  policyregistry.ObjectRef
	ReleaseSHA256 string
	objects       map[string][]byte
}

func NewServingGraph(classifierURL string) (ServingGraph, error) {
	policyPayload := []byte(policyJSON)
	policySHA := policyregistry.Digest(policyPayload)
	policyRelative := "router_policy/v1/policies/sha256/" + policySHA + ".json"
	defaultPolicy := policyregistry.PolicyObject{URI: registryRoot + "/" + policyRelative, SHA256: policySHA, SchemaVersion: rosterdata.SchemaVersionPolicyV1, Generation: 1}
	candidate, err := canonicalArtifact(policyregistry.CandidateV3{
		SchemaVersion:     policyregistry.ServingCandidateV3,
		RouterImageDigest: ServingImageDigest,
		Requirements:      policyregistry.BuiltRouterCandidateRequirements(),
		Provenance:        policyregistry.ServingProvenance{RouterRevision: servingSourceRevision, WeaveRevision: servingSourceRevision, BuildAttestation: artifactRef("hmm-stub-router-build")},
	})
	if err != nil {
		return ServingGraph{}, fmt.Errorf("encode stub candidate: %w", err)
	}
	release := classifierRelease()
	releaseObject, err := canonicalArtifact(release)
	if err != nil {
		return ServingGraph{}, fmt.Errorf("encode stub classifier release: %w", err)
	}
	selection, err := canonicalArtifact(policyregistry.SelectionSetV4{
		SchemaVersion: policyregistry.ServingSelectionSetV4,
		Target:        ServingTarget,
		Code: policyregistry.CandidateCodeV4{
			Candidate: candidate.ref, Project: ServingProject, Region: ServingRegion,
			Router:      policyregistry.RevisionBinding{Name: ServingRevision, URL: servingRouterOrigin, Audience: servingRouterOrigin, ImageDigest: ServingImageDigest, Configuration: ServingConfiguration()},
			Attestation: artifactRef("hmm-stub-router-attestation"),
		},
		Classifier:       policyregistry.ClassifierServiceBinding{Release: releaseObject.ref, EnabledVariants: []policy.ClassifierVariantID{policy.ClassifierVariantPrimary}},
		DefaultPolicy:    defaultPolicy,
		StrategyPolicies: map[router.Strategy]policyregistry.PolicyObject{},
		Profiles:         map[string]policyregistry.PolicyObject{},
	})
	if err != nil {
		return ServingGraph{}, fmt.Errorf("encode stub selection set: %w", err)
	}
	state, err := json.Marshal(policyregistry.ServingControlState{
		SchemaVersion: policyregistry.ServingControlStateV1, Target: ServingTarget, CurrentActivationID: servingActivationID, Sequence: 1,
		Activations: map[string]policyregistry.Activation{servingActivationID: {
			ID: servingActivationID, Sequence: 1, SelectionSet: selection.ref, ActivatedAt: servingCreatedAt,
			Proposal: artifactRef("hmm-stub-proposal"), RequestID: "hmm-stub", Actor: "hmm-stub", WorkflowActor: "hmm-stub",
		}},
	})
	if err != nil {
		return ServingGraph{}, fmt.Errorf("encode stub serving state: %w", err)
	}
	environment, err := ServingTarget.Environment()
	if err != nil {
		return ServingGraph{}, err
	}
	binding, err := json.Marshal(policyregistry.ClassifierRuntimeBinding{
		SchemaVersion: policyregistry.ClassifierRuntimeBindingSchemaV1, Environment: environment, ProjectID: ServingProject,
		ReleaseSHA256: releaseObject.ref.SHA256, WorkerRevision: "hmm-stub", WorkerURL: classifierURL, WorkerAudience: classifierURL,
		ImageDigest: release.RuntimeImageDigest, ConfigurationSHA256: strings.Repeat("f", 64),
	})
	if err != nil {
		return ServingGraph{}, fmt.Errorf("encode stub runtime binding: %w", err)
	}
	statePath, err := policyregistry.DirectoryServingStatePath(ServingTarget)
	if err != nil {
		return ServingGraph{}, err
	}
	return ServingGraph{
		SelectionSet:  selection.ref,
		ReleaseSHA256: releaseObject.ref.SHA256,
		objects: map[string][]byte{
			policyRelative:         policyPayload,
			candidate.relative:     candidate.payload,
			releaseObject.relative: releaseObject.payload,
			selection.relative:     selection.payload,
			statePath:              state,
			policyregistry.DirectoryServingRuntimeBindingPath(environment, releaseObject.ref.SHA256): binding,
		},
	}, nil
}

func (g ServingGraph) Objects() map[string][]byte {
	return maps.Clone(g.objects)
}

func (g ServingGraph) Write(dir string) error {
	return policyregistry.WriteDirectoryServingRegistry(dir, registryRoot, g.objects)
}

func handleV5(mux *http.ServeMux) {
	identity := classifier()
	mux.HandleFunc("GET /v5/capabilities", func(w http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		writeJSON(w, policyclient.ClassifierCapabilitiesV5{
			SchemaVersion: policy.SchemaVersionV5, APIMajor: policy.ClassifierAPIMajorV5,
			ClassifierSelectionID: query.Get("classifier_selection_id"), ClassifierReleaseID: query.Get("classifier_release_id"),
			WireSchema: policy.SchemaVersionV5, ClassOrder: identity.ClassOrder, TaxonomySHA256: identity.TaxonomySHA256,
			ClassifierVariants: []policy.ClassifierVariantID{policy.ClassifierVariantPrimary}, SupportsPreview: true,
		})
	})
	classify := func(w http.ResponseWriter, request *http.Request) {
		var classifyRequest struct {
			RequestID             string                     `json:"request_id"`
			ClassifierSelectionID string                     `json:"classifier_selection_id"`
			ClassifierReleaseID   string                     `json:"classifier_release_id"`
			ClassifierVariantID   policy.ClassifierVariantID `json:"classifier_variant_id"`
		}
		if err := json.NewDecoder(request.Body).Decode(&classifyRequest); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"schema_version": policy.SchemaVersionV5, "request_id": classifyRequest.RequestID,
			"classifier_selection_id": classifyRequest.ClassifierSelectionID, "classifier_release_id": classifyRequest.ClassifierReleaseID,
			"classifier_variant_id": classifyRequest.ClassifierVariantID, "classifier_artifact_id": identity.ArtifactID,
			"classifier_artifact_sha256": identity.PackageSHA256, "classifier_taxonomy_sha256": identity.TaxonomySHA256,
			"predicted_label": ClassLabel, "class_order": identity.ClassOrder,
			"class_probabilities": map[string]float64{ClassLabel: 1}, "classifier_confidence": 1.0, "classifier_margin": 1.0,
		})
	}
	mux.HandleFunc("POST /v5/classify", classify)
	mux.HandleFunc("POST /v5/preview", classify)
}
