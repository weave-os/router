package policyregistry

import (
	"context"
	"errors"
)

// WorkerIdentity is release-owned boot configuration attested during preparation.
// A policy-only release can reuse it; a different image, target or revision cannot.
type WorkerIdentity struct {
	Target        ServingTarget
	Project       string
	Region        string
	Revision      string
	ImageDigest   string
	Configuration ObjectRef
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

// ResolveAdmissionBinding validates the target-local destination without consulting a newer head.
// Reading lifecycle again here would change the decision of an already admitted request.
func ResolveAdmissionBinding(ctx context.Context, store ServingStore, admission SessionReleaseBinding) (DeploymentBinding, error) {
	if err := admission.Selection.validate(store.RootURI(), admission.ProfileKey != ""); err != nil {
		return DeploymentBinding{}, err
	}
	binding, err := readServing[*DeploymentBinding](ctx, store, ServingBindings, admission.Selection.Binding)
	if err != nil {
		return DeploymentBinding{}, err
	}
	if binding.Target != admission.Target || binding.Release != admission.Selection.Release {
		return DeploymentBinding{}, errors.New("admitted destination differs from immutable binding")
	}
	return *binding, nil
}

// ValidateWorkerAdmission rejects a signed request addressed to a different physical worker.
func ValidateWorkerAdmission(ctx context.Context, store ServingStore, identity WorkerIdentity, assertion ServingAssertion, installationID, apiKeyID string) (DeploymentBinding, error) {
	if err := identity.Validate(); err != nil {
		return DeploymentBinding{}, err
	}
	if assertion.Scope.InstallationID != installationID || assertion.APIKeyID != apiKeyID || assertion.Admission.Target != identity.Target {
		return DeploymentBinding{}, errors.New("serving assertion does not match authenticated credential or worker target")
	}
	binding, err := ResolveAdmissionBinding(ctx, store, assertion.Admission)
	if err != nil {
		return DeploymentBinding{}, err
	}
	if binding.Project != identity.Project || binding.Region != identity.Region || binding.Router.Name != identity.Revision || binding.Router.ImageDigest != identity.ImageDigest || binding.Router.Configuration != identity.Configuration {
		return DeploymentBinding{}, errors.New("admission binding differs from attested worker identity")
	}
	return binding, nil
}
