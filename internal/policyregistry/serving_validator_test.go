package policyregistry_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

type staticCatalog map[string]struct{}

func (c staticCatalog) ContainsArm(arm string) bool {
	_, ok := c[arm]
	return ok
}

type staticAttestor struct {
	attestation policyregistry.ClassifierAttestation
}

func (a staticAttestor) Attest(context.Context, string) (policyregistry.ClassifierAttestation, error) {
	return a.attestation, nil
}

type staticProber struct{}

func (staticProber) Probe(context.Context, policyregistry.DeploymentBinding) error { return nil }

func TestDestinationValidatorRejectsUnknownCatalogArms(t *testing.T) {
	store, _, set := controllerFixture(t)
	prepared, err := policyregistry.ReadPreparedSelection(context.Background(), store, policyregistry.TargetStable, "", set.Default)
	require.NoError(t, err)
	attestor := staticAttestor{attestation: policyregistry.ClassifierAttestation{
		ArtifactID:     prepared.Classifier.Identity.ArtifactID,
		PackageSHA256:  prepared.Classifier.Identity.PackageSHA256,
		ImageDigest:    prepared.Classifier.Identity.ImageDigest,
		WireSchema:     prepared.Classifier.Identity.WireSchema,
		TaxonomySHA256: prepared.Classifier.Identity.TaxonomySHA256,
		ClassOrder:     append([]string(nil), prepared.Classifier.Identity.ClassOrder...),
	}}
	validator := policyregistry.DestinationValidator{Catalog: staticCatalog{}, Attestor: attestor, Prober: staticProber{}}
	require.Error(t, validator.ValidatePreparedSelection(context.Background(), prepared, nil))
	arms := staticCatalog{}
	for _, arm := range prepared.Policy.AllArms() {
		arms[arm] = struct{}{}
	}
	validator.Catalog = arms
	require.NoError(t, validator.ValidatePreparedSelection(context.Background(), prepared, nil))
}
