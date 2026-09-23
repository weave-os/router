package policyregistry

import (
	"context"
	"errors"
)

// WorkerIdentity is release-owned boot configuration attested during preparation.
// A policy-only release can reuse it; a different image, target or revision cannot.
type WorkerIdentity struct {
	Target        ServingTarget `json:"target"`
	Project       string        `json:"project"`
	Region        string        `json:"region"`
	Revision      string        `json:"revision"`
	ImageDigest   string        `json:"image_digest"`
	Configuration ObjectRef     `json:"configuration"`
}

// Validate requires exact local identity before the managed worker mounts inference endpoints.
func (w WorkerIdentity) Validate() error {
	if _, err := w.Target.Environment(); err != nil {
		return err
	}
	if w.Project == "" || w.Region == "" || w.Revision == "" || !validImageDigest(w.ImageDigest) {
		return errors.New("managed worker requires exact attested boot identity")
	}
	return validateArtifactRef(w.Configuration)
}

// ValidateBinding enforces physical identity for both preparation and request admission.
func (w WorkerIdentity) ValidateBinding(target ServingTarget, binding LaneBinding) error {
	if err := w.Validate(); err != nil {
		return err
	}
	if target != w.Target || binding.Project != w.Project || binding.Region != w.Region || binding.Router.Name != w.Revision || binding.Router.ImageDigest != w.ImageDigest || binding.Router.Configuration != w.Configuration {
		return errors.New("admission binding differs from attested worker identity")
	}
	return nil
}

// ResolveAdmissionBinding validates the target-local destination without consulting a newer head.
// Reading lifecycle again here would change the decision of an already admitted request.
func ResolveAdmissionBinding(ctx context.Context, store ServingStore, admission SessionReleaseBinding) (LaneBinding, error) {
	if err := admission.Selection.validate(store.RootURI(), admission.ProfileKey != ""); err != nil {
		return LaneBinding{}, err
	}
	if admission.Selection.isLane(store.RootURI()) {
		lane, err := readLane(ctx, store, admission.Target, admission.ProfileKey, admission.Selection)
		if err != nil {
			return LaneBinding{}, err
		}
		return lane.LaneBinding, nil
	}
	binding, err := readServing[*DeploymentBinding](ctx, store, ServingBindings, admission.Selection.Binding)
	if err != nil {
		return LaneBinding{}, err
	}
	if binding.Target != admission.Target || binding.Release != admission.Selection.Release {
		return LaneBinding{}, errors.New("admitted destination differs from immutable binding")
	}
	return binding.lane(), nil
}

// ValidateWorkerAdmission rejects a signed request addressed to a different physical worker.
func ValidateWorkerAdmission(ctx context.Context, store ServingStore, identity WorkerIdentity, assertion ServingAssertion, installationID, apiKeyID string) (LaneBinding, error) {
	if err := identity.Validate(); err != nil {
		return LaneBinding{}, err
	}
	if assertion.Scope.InstallationID != installationID || assertion.APIKeyID != apiKeyID || assertion.Admission.Target != identity.Target {
		return LaneBinding{}, errors.New("serving assertion does not match authenticated credential or worker target")
	}
	binding, err := ResolveAdmissionBinding(ctx, store, assertion.Admission)
	if err != nil {
		return LaneBinding{}, err
	}
	if err := identity.ValidateBinding(assertion.Admission.Target, binding); err != nil {
		return LaneBinding{}, err
	}
	return binding, nil
}

// ValidateWorkerSelection exercises the destination's real loader without provider calls.
// IAM validation identities can prepare snapshots but cannot mint admission assertions.
func ValidateWorkerSelection(ctx context.Context, store ServingStore, cache *ServingRuntimeCache, identity WorkerIdentity, request WorkerValidationRequest) (WorkerAttestation, error) {
	if request.Target != identity.Target {
		return WorkerAttestation{}, errors.New("validation target differs from worker identity")
	}
	prepared, err := ReadPreparedSelection(ctx, store, request.Target, request.ProfileKey, request.Selection)
	if err != nil {
		return WorkerAttestation{}, err
	}
	if err := identity.ValidateBinding(prepared.Target, prepared.Binding); err != nil {
		return WorkerAttestation{}, err
	}
	snapshot, err := cache.Snapshot(ctx, SessionReleaseBinding{Target: request.Target, ProfileKey: request.ProfileKey, Selection: request.Selection})
	if err != nil {
		return WorkerAttestation{}, err
	}
	return WorkerAttestation{Identity: identity, Requirements: prepared.Candidate.Requirements, CatalogArms: snapshot.Policy.AllArms(), Selection: request.Selection, Ready: true}, nil
}
