package policyregistry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
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
	Identity     WorkerIdentity      `json:"identity"`
	Requirements ServingRequirements `json:"requirements"`
	CatalogArms  []string            `json:"catalog_arms"`
	Selection    ServingSelection    `json:"selection"`
	Ready        bool                `json:"ready"`
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

// DestinationValidator validates destination runtime capabilities rather than the controller's catalog.
type DestinationValidator struct {
	Endpoints DestinationEndpoints
}

// ValidatePreparedSelection requires complete classifier attestation and an exact worker snapshot smoke.
// ServingController verifies immutable proposal evidence before invoking this destination-only validator.
func (v DestinationValidator) ValidatePreparedSelection(ctx context.Context, prepared PreparedSelection) error {
	if v.Endpoints == nil {
		return errors.New("destination validator requires private revision endpoints")
	}
	classifier, err := v.Endpoints.AttestClassifier(ctx, prepared.Binding.Classifier)
	if err != nil {
		return fmt.Errorf("attest classifier revision %q (full serving attestation is required): %w", prepared.Binding.Classifier.Name, err)
	}
	identity := prepared.Candidate.Classifier.Identity
	if !classifier.Ready || classifier.Revision != prepared.Binding.Classifier.Name || classifier.Identity.ArtifactID != identity.ArtifactID || classifier.Identity.PackageSHA256 != identity.PackageSHA256 || classifier.Identity.ImageDigest != identity.ImageDigest || classifier.Identity.WireSchema != identity.WireSchema || classifier.Identity.TaxonomySHA256 != identity.TaxonomySHA256 || !slices.Equal(classifier.Identity.ClassOrder, identity.ClassOrder) {
		return errors.New("classifier readiness, revision or core identity does not match the selected bundle")
	}
	if classifier.Package != prepared.Candidate.Classifier.Package || classifier.Configuration != prepared.Candidate.Classifier.Configuration || classifier.AuxiliaryModels == nil || !maps.Equal(classifier.AuxiliaryModels, prepared.Candidate.Classifier.AuxiliaryModels) {
		return errors.New("classifier loaded package, auxiliary model inventory or configuration differs from the selected bundle")
	}
	worker, err := v.Endpoints.ValidateWorker(ctx, prepared.Binding.Router, WorkerValidationRequest{Target: prepared.Target, ProfileKey: prepared.ProfileKey, Selection: prepared.Selection})
	if err != nil {
		return fmt.Errorf("validate destination worker revision %q: %w", prepared.Binding.Router.Name, err)
	}
	expected := WorkerIdentity{Target: prepared.Target, Project: prepared.Binding.Project, Region: prepared.Binding.Region, Revision: prepared.Binding.Router.Name, ImageDigest: prepared.Binding.Router.ImageDigest, Configuration: prepared.Binding.Router.Configuration}
	if !worker.Ready || worker.Identity != expected || worker.Requirements != prepared.Candidate.Requirements || !sameSelection(worker.Selection, prepared.Selection) {
		return errors.New("worker readiness, identity, requirements or validated snapshot differs from the prepared selection")
	}
	for _, arm := range prepared.Policy.AllArms() {
		if !slices.Contains(worker.CatalogArms, arm) {
			return fmt.Errorf("selected policy arm %q is absent from the destination worker catalog", arm)
		}
	}
	return nil
}
