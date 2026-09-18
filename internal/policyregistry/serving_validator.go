package policyregistry

import (
	"context"
	"errors"
	"fmt"
)

// WorkerCatalog is the destination worker's own routable model set, never the controller compile catalog.
type WorkerCatalog interface {
	ContainsArm(arm string) bool
}

// ClassifierAttestation is the identity reported by a private classifier revision.
type ClassifierAttestation struct {
	ArtifactID     string
	PackageSHA256  string
	ImageDigest    string
	WireSchema     string
	TaxonomySHA256 string
	ClassOrder     []string
}

// ClassifierAttestor probes the selected classifier revision over its private endpoint.
type ClassifierAttestor interface {
	Attest(ctx context.Context, revisionURL string) (ClassifierAttestation, error)
}

// PrivateEndpointProber confirms the prepared worker/classifier revisions answer privately.
type PrivateEndpointProber interface {
	Probe(ctx context.Context, binding DeploymentBinding) error
}

// DestinationValidator is the production ServingValidator: attestation, destination catalog and private smoke.
type DestinationValidator struct {
	Catalog  WorkerCatalog
	Attestor ClassifierAttestor
	Prober   PrivateEndpointProber
}

// ValidatePreparedSelection rejects unknown arms and classifier/schema mismatches before activation.
func (v DestinationValidator) ValidatePreparedSelection(ctx context.Context, prepared PreparedSelection, _ []ObjectRef) error {
	if v.Catalog == nil || v.Attestor == nil || v.Prober == nil {
		return errors.New("destination validator requires catalog, attestor and private endpoint probe")
	}
	attestation, err := v.Attestor.Attest(ctx, prepared.Binding.Classifier.URL)
	if err != nil {
		return fmt.Errorf("attest classifier revision: %w", err)
	}
	identity := prepared.Classifier.Identity
	switch {
	case attestation.ArtifactID != identity.ArtifactID:
		return errors.New("classifier artifact does not match the selected bundle")
	case attestation.PackageSHA256 != identity.PackageSHA256:
		return errors.New("classifier package digest does not match the selected bundle")
	case attestation.ImageDigest != identity.ImageDigest:
		return errors.New("classifier image digest does not match the selected bundle")
	case attestation.WireSchema != identity.WireSchema:
		return errors.New("classifier wire schema does not match the selected bundle")
	case attestation.TaxonomySHA256 != identity.TaxonomySHA256:
		return errors.New("classifier taxonomy digest does not match the selected bundle")
	}
	if len(attestation.ClassOrder) != len(identity.ClassOrder) {
		return errors.New("classifier class order does not match the selected bundle")
	}
	for i := range identity.ClassOrder {
		if attestation.ClassOrder[i] != identity.ClassOrder[i] {
			return errors.New("classifier class order does not match the selected bundle")
		}
	}
	for _, arm := range prepared.Policy.AllArms() {
		if !v.Catalog.ContainsArm(arm) {
			return fmt.Errorf("selected policy arm %q is absent from the destination worker catalog", arm)
		}
	}
	return v.Prober.Probe(ctx, prepared.Binding)
}
