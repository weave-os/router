package policyregistry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"weave-os/router/internal/router/policy"
)

const (
	// WorkerValidationPath loads and validates an exact snapshot without admitting inference.
	WorkerValidationPath = "/internal/serving/validate"
	// ClassifierAttestationPath must report loaded models/configuration, not expected environment values.
	ClassifierAttestationPath = "/internal/serving/attestation"
)

// WorkerValidationRequest never selects a mutable head or accepts caller-supplied policy bytes.
type WorkerValidationRequest struct {
	Target     ServingTarget    `json:"target"`
	ProfileKey string           `json:"profile_key,omitempty"`
	Selection  ServingSelection `json:"selection"`
}

// WorkerAttestation reports capabilities from the selected destination binary after snapshot smoke.
type WorkerAttestation struct {
	Identity           WorkerIdentity              `json:"identity"`
	Requirements       ServingRequirements         `json:"requirements"`
	SupportedAPIMajors []policy.ClassifierAPIMajor `json:"supported_api_majors,omitempty"`
	CatalogArms        []string                    `json:"catalog_arms"`
	Selection          ServingSelection            `json:"selection"`
	Ready              bool                        `json:"ready"`
}

// ClassifierServiceAttestation is the strict v5 capability contract returned by a classifier worker.
type ClassifierServiceAttestation struct {
	SchemaVersion         string                       `json:"schema_version"`
	APIMajor              policy.ClassifierAPIMajor    `json:"api_major"`
	ClassifierSelectionID string                       `json:"classifier_selection_id"`
	ClassifierReleaseID   string                       `json:"classifier_release_id"`
	WireSchema            string                       `json:"wire_schema"`
	ClassOrder            []string                     `json:"class_order"`
	TaxonomySHA256        string                       `json:"classifier_taxonomy_sha256"`
	ClassifierVariants    []policy.ClassifierVariantID `json:"classifier_variants"`
	SupportsPreview       bool                         `json:"supports_preview"`
}

// ClassifierWorkerAttestationSchemaV1 is the worker revision identity schema.
const ClassifierWorkerAttestationSchemaV1 = "router_classifier_worker_attestation_v1"

// ClassifierWorkerReady is the only status a worker may report for activation.
const ClassifierWorkerReady = "ready"

// ClassifierWorkerVariantAttestation is one variant artifact a worker revision loaded.
type ClassifierWorkerVariantAttestation struct {
	ArtifactID     string `json:"artifact_id"`
	ArtifactSHA256 string `json:"artifact_sha256"`
}

// ClassifierWorkerAttestation reports the release, image and configuration one tagged worker revision loaded.
type ClassifierWorkerAttestation struct {
	SchemaVersion       string                                                            `json:"schema_version"`
	Status              string                                                            `json:"status"`
	Environment         Environment                                                       `json:"environment"`
	ReleaseID           string                                                            `json:"release_id"`
	WorkerRevision      string                                                            `json:"worker_revision"`
	ImageDigest         string                                                            `json:"image_digest"`
	ConfigurationSHA256 string                                                            `json:"configuration_sha256"`
	Variants            map[policy.ClassifierVariantID]ClassifierWorkerVariantAttestation `json:"variants"`
}

// ClassifierAttestation includes the complete loaded bundle so auxiliary/config changes cannot hide behind image reuse.
type ClassifierAttestation struct {
	Identity        ClassifierIdentity   `json:"identity"`
	Package         ObjectRef            `json:"package"`
	AuxiliaryModels map[string]ObjectRef `json:"auxiliary_models"`
	Configuration   ObjectRef            `json:"configuration"`
	Revision        string               `json:"revision"`
	Ready           bool                 `json:"ready"`
}

// DestinationEndpoints performs authenticated private calls to the exact prepared revisions.
type DestinationEndpoints interface {
	ValidateWorker(context.Context, RevisionBinding, WorkerValidationRequest) (WorkerAttestation, error)
	AttestClassifier(context.Context, RevisionBinding) (ClassifierAttestation, error)
}

// ClassifierServiceAttestationEndpoints validates a logical release on the worker revision its
// runtime binding names: v5 capabilities for the selection, then the worker's loaded identity.
type ClassifierServiceAttestationEndpoints interface {
	AttestClassifierService(context.Context, ClassifierRuntimeBinding, string) (ClassifierServiceAttestation, error)
	AttestClassifierWorker(context.Context, ClassifierRuntimeBinding) (ClassifierWorkerAttestation, error)
}

// DestinationValidator validates destination runtime capabilities rather than the controller's catalog.
type DestinationValidator struct {
	Endpoints DestinationEndpoints
}

// ValidationStats reports how many destination attestations one activation issued.
type ValidationStats struct {
	HTTPCalls int
	CacheHits int
}

// ActivationValidator validates every lane of a single activation and reports its destination
// attestation counts. Implementations may memoize attestations across the lanes of that one
// activation; they must never outlive it.
type ActivationValidator interface {
	ServingValidator
	Stats() ValidationStats
}

// ActivationScopedValidator builds a fresh ActivationValidator for one proposal validation.
type ActivationScopedValidator interface {
	BeginActivation() ActivationValidator
}

// BeginActivation returns a validator whose destination attestations are memoized for the lanes
// of one activation only.
func (v DestinationValidator) BeginActivation() ActivationValidator {
	return &activationValidator{
		endpoints: v.Endpoints, classifiers: map[RevisionBinding]classifierAttestationResult{},
		classifierServices: map[classifierServiceAttestationKey]classifierServiceAttestationResult{},
		classifierWorkers:  map[classifierWorkerKey]classifierWorkerAttestationResult{},
		workers:            map[workerAttestationKey]workerAttestationResult{},
	}
}

// workerAttestationKey identifies one worker attestation. ValidateWorkerSelection derives the
// attested requirements, catalog arms and echoed snapshot from the request, so the key carries
// the full request as well as the revision identity it was sent to. ServingSelection holds a
// profile pointer, so the key stores that reference by value and presence.
type workerAttestationKey struct {
	Binding    RevisionBinding
	Target     ServingTarget
	ProfileKey string
	Release    ObjectRef
	Selection  ObjectRef
	Profile    ObjectRef
	HasProfile bool
}

func newWorkerAttestationKey(binding RevisionBinding, request WorkerValidationRequest) workerAttestationKey {
	key := workerAttestationKey{Binding: binding, Target: request.Target, ProfileKey: request.ProfileKey, Release: request.Selection.Release, Selection: request.Selection.Binding}
	if request.Selection.Profile != nil {
		key.Profile, key.HasProfile = *request.Selection.Profile, true
	}
	return key
}

type classifierAttestationResult struct {
	attestation ClassifierAttestation
	err         error
}

type workerAttestationResult struct {
	attestation WorkerAttestation
	err         error
}

// activationValidator memoizes raw destination attestations, keyed by the complete revision
// identity they were obtained from, for the lanes of one activation. Failures are memoized too:
// an unreachable or rejecting destination already fails every lane today, so replaying the stored
// failure keeps the activation closed instead of admitting a lane on an endpoint retry. Every
// per-lane comparison still runs against each lane's own candidate composition.
type activationValidator struct {
	endpoints          DestinationEndpoints
	classifiers        map[RevisionBinding]classifierAttestationResult
	classifierServices map[classifierServiceAttestationKey]classifierServiceAttestationResult
	classifierWorkers  map[classifierWorkerKey]classifierWorkerAttestationResult
	workers            map[workerAttestationKey]workerAttestationResult
	stats              ValidationStats
}

// classifierWorkerKey is the comparable dispatch identity of a runtime binding.
type classifierWorkerKey struct {
	Environment         Environment
	ReleaseSHA256       string
	WorkerRevision      string
	WorkerURL           string
	WorkerAudience      string
	ImageDigest         string
	ConfigurationSHA256 string
}

func newClassifierWorkerKey(binding ClassifierRuntimeBinding) classifierWorkerKey {
	return classifierWorkerKey{
		Environment: binding.Environment, ReleaseSHA256: binding.ReleaseSHA256, WorkerRevision: binding.WorkerRevision,
		WorkerURL: binding.WorkerURL, WorkerAudience: binding.WorkerAudience, ImageDigest: binding.ImageDigest,
		ConfigurationSHA256: binding.ConfigurationSHA256,
	}
}

type classifierServiceAttestationKey struct {
	Worker      classifierWorkerKey
	SelectionID string
}

type classifierWorkerAttestationResult struct {
	attestation ClassifierWorkerAttestation
	err         error
}

type classifierServiceAttestationResult struct {
	attestation ClassifierServiceAttestation
	err         error
}

func (a *activationValidator) ValidatePreparedSelection(ctx context.Context, prepared PreparedSelection) error {
	if a.endpoints == nil {
		return errors.New("destination validator requires private revision endpoints")
	}
	return DestinationValidator{Endpoints: a}.ValidatePreparedSelection(ctx, prepared)
}

func (a *activationValidator) Stats() ValidationStats { return a.stats }

func (a *activationValidator) AttestClassifier(ctx context.Context, binding RevisionBinding) (ClassifierAttestation, error) {
	if memoized, exists := a.classifiers[binding]; exists {
		a.stats.CacheHits++
		return memoized.attestation, memoized.err
	}
	a.stats.HTTPCalls++
	attestation, err := a.endpoints.AttestClassifier(ctx, binding)
	a.classifiers[binding] = classifierAttestationResult{attestation: attestation, err: err}
	return attestation, err
}

func (a *activationValidator) AttestClassifierService(ctx context.Context, binding ClassifierRuntimeBinding, selectionID string) (ClassifierServiceAttestation, error) {
	key := classifierServiceAttestationKey{Worker: newClassifierWorkerKey(binding), SelectionID: selectionID}
	if memoized, exists := a.classifierServices[key]; exists {
		a.stats.CacheHits++
		return memoized.attestation, memoized.err
	}
	endpoints, ok := a.endpoints.(ClassifierServiceAttestationEndpoints)
	if !ok {
		return ClassifierServiceAttestation{}, errClassifierServiceEndpointsRequired
	}
	a.stats.HTTPCalls++
	attestation, err := endpoints.AttestClassifierService(ctx, binding, selectionID)
	a.classifierServices[key] = classifierServiceAttestationResult{attestation: attestation, err: err}
	return attestation, err
}

func (a *activationValidator) AttestClassifierWorker(ctx context.Context, binding ClassifierRuntimeBinding) (ClassifierWorkerAttestation, error) {
	key := newClassifierWorkerKey(binding)
	if memoized, exists := a.classifierWorkers[key]; exists {
		a.stats.CacheHits++
		return memoized.attestation, memoized.err
	}
	endpoints, ok := a.endpoints.(ClassifierServiceAttestationEndpoints)
	if !ok {
		return ClassifierWorkerAttestation{}, errClassifierServiceEndpointsRequired
	}
	a.stats.HTTPCalls++
	attestation, err := endpoints.AttestClassifierWorker(ctx, binding)
	a.classifierWorkers[key] = classifierWorkerAttestationResult{attestation: attestation, err: err}
	return attestation, err
}

var errClassifierServiceEndpointsRequired = errors.New("classifier worker attestation endpoints are required for v5 selections")

func (a *activationValidator) ValidateWorker(ctx context.Context, binding RevisionBinding, request WorkerValidationRequest) (WorkerAttestation, error) {
	key := newWorkerAttestationKey(binding, request)
	if memoized, exists := a.workers[key]; exists {
		a.stats.CacheHits++
		return memoized.attestation, memoized.err
	}
	a.stats.HTTPCalls++
	attestation, err := a.endpoints.ValidateWorker(ctx, binding, request)
	a.workers[key] = workerAttestationResult{attestation: attestation, err: err}
	return attestation, err
}

// ValidatePreparedSelection requires complete classifier attestation and an exact worker snapshot smoke.
// ServingController verifies immutable proposal evidence before invoking this destination-only validator.
func (v DestinationValidator) ValidatePreparedSelection(ctx context.Context, prepared PreparedSelection) error {
	if v.Endpoints == nil {
		return errors.New("destination validator requires private revision endpoints")
	}
	if prepared.ClassifierServiceBinding != nil {
		if err := validateClassifierServiceAttestation(ctx, v.Endpoints, prepared); err != nil {
			return err
		}
	} else {
		classifier, err := v.Endpoints.AttestClassifier(ctx, prepared.Binding.Classifier)
		if err != nil {
			return fmt.Errorf("attest classifier revision %q (full serving attestation is required): %w", prepared.Binding.Classifier.Name, err)
		}
		identity := prepared.Candidate.Classifier.Identity
		if !classifier.Ready || classifier.Revision != prepared.Binding.Classifier.Name || !classifier.Identity.Equal(identity) {
			return errors.New("classifier readiness, revision or core identity does not match the selected bundle")
		}
		if classifier.Package != prepared.Candidate.Classifier.Package || classifier.Configuration != prepared.Candidate.Classifier.Configuration || classifier.AuxiliaryModels == nil || !maps.Equal(classifier.AuxiliaryModels, prepared.Candidate.Classifier.AuxiliaryModels) {
			return errors.New("classifier loaded package, auxiliary model inventory or configuration differs from the selected bundle")
		}
	}
	worker, err := v.Endpoints.ValidateWorker(ctx, prepared.Binding.Router, WorkerValidationRequest{Target: prepared.Target, ProfileKey: prepared.ProfileKey, Selection: prepared.Selection})
	if err != nil {
		return fmt.Errorf("validate destination worker revision %q: %w", prepared.Binding.Router.Name, err)
	}
	expected := WorkerIdentity{Target: prepared.Target, Project: prepared.Binding.Project, Region: prepared.Binding.Region, Revision: prepared.Binding.Router.Name, ImageDigest: prepared.Binding.Router.ImageDigest, Configuration: prepared.Binding.Router.Configuration}
	if !worker.Ready || worker.Identity != expected || worker.Requirements != prepared.Candidate.Requirements || !sameSelection(worker.Selection, prepared.Selection) {
		return errors.New("worker readiness, identity, requirements or validated snapshot differs from the prepared selection")
	}
	for _, major := range prepared.Candidate.SupportedAPIMajors {
		if !slices.Contains(worker.SupportedAPIMajors, major) {
			return fmt.Errorf("worker does not support candidate classifier API major %d", major)
		}
	}
	for _, arm := range prepared.Policy.AllArms() {
		if !slices.Contains(worker.CatalogArms, arm) {
			return fmt.Errorf("selected policy arm %q is absent from the destination worker catalog", arm)
		}
	}
	for strategy, strategyPolicy := range prepared.StrategyPolicies {
		for _, arm := range strategyPolicy.Roster.AllArms() {
			if !slices.Contains(worker.CatalogArms, arm) {
				return fmt.Errorf("%s strategy policy arm %q is absent from the destination worker catalog", strategy, arm)
			}
		}
	}
	return nil
}

func validateClassifierServiceAttestation(ctx context.Context, endpoints DestinationEndpoints, prepared PreparedSelection) error {
	if prepared.ClassifierReleaseV5 == nil || prepared.ClassifierRuntimeBinding == nil {
		return errors.New("v5 selection has no logical classifier release or runtime binding")
	}
	attestor, ok := endpoints.(ClassifierServiceAttestationEndpoints)
	if !ok {
		return errClassifierServiceEndpointsRequired
	}
	selectionID := prepared.Selection.Binding.SHA256
	release := prepared.ClassifierReleaseV5
	releaseID := prepared.ClassifierServiceBinding.Release.SHA256
	binding := prepared.ClassifierRuntimeBinding.Binding
	worker, err := attestor.AttestClassifierWorker(ctx, binding)
	if err != nil {
		return fmt.Errorf("attest classifier worker revision %q: %w", binding.WorkerRevision, err)
	}
	if worker.SchemaVersion != ClassifierWorkerAttestationSchemaV1 || worker.Status != ClassifierWorkerReady || worker.Environment != binding.Environment ||
		worker.ReleaseID != releaseID || worker.WorkerRevision != binding.WorkerRevision || worker.ImageDigest != binding.ImageDigest ||
		worker.ConfigurationSHA256 != binding.ConfigurationSHA256 {
		return errors.New("classifier worker readiness, revision, image or configuration differs from the release runtime binding")
	}
	for _, variantID := range prepared.ClassifierServiceBinding.EnabledVariants {
		loaded, expected := worker.Variants[variantID], release.Variants[variantID]
		if loaded.ArtifactID != expected.ArtifactID || loaded.ArtifactSHA256 != expected.ArtifactSHA256 {
			return fmt.Errorf("classifier worker variant %q artifact differs from the selected release", variantID)
		}
	}
	attestation, err := attestor.AttestClassifierService(ctx, binding, selectionID)
	if err != nil {
		return fmt.Errorf("attest logical classifier service release %q: %w", releaseID, err)
	}
	if attestation.SchemaVersion != policy.SchemaVersionV5 || attestation.APIMajor != release.APIMajor || attestation.WireSchema != release.WireSchema ||
		attestation.ClassifierSelectionID != selectionID || attestation.ClassifierReleaseID != releaseID ||
		attestation.TaxonomySHA256 != release.Taxonomy.SHA256 || !slices.Equal(attestation.ClassOrder, release.Taxonomy.ClassOrder) ||
		!slices.Equal(attestation.ClassifierVariants, prepared.ClassifierServiceBinding.EnabledVariants) || !attestation.SupportsPreview {
		return errors.New("classifier worker readiness or v5 capabilities differ from the selected release binding")
	}
	return nil
}
