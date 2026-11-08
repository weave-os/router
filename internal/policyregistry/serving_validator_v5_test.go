package policyregistry_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/policy"
)

type classifierWorkerEndpoints struct {
	staticDestinationEndpoints
	capabilities policyregistry.ClassifierServiceAttestation
	worker       policyregistry.ClassifierWorkerAttestation
	bindings     []policyregistry.ClassifierRuntimeBinding
}

func (e *classifierWorkerEndpoints) AttestClassifierService(_ context.Context, binding policyregistry.ClassifierRuntimeBinding, _ string) (policyregistry.ClassifierServiceAttestation, error) {
	e.bindings = append(e.bindings, binding)
	return e.capabilities, nil
}

func (e *classifierWorkerEndpoints) AttestClassifierWorker(_ context.Context, binding policyregistry.ClassifierRuntimeBinding) (policyregistry.ClassifierWorkerAttestation, error) {
	e.bindings = append(e.bindings, binding)
	return e.worker, nil
}

func v5PreparedSelection() (policyregistry.PreparedSelection, *classifierWorkerEndpoints) {
	releaseID, selectionID, artifactSHA := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("d", 64)
	classOrder := []string{"fast", "maximum"}
	release := &policyregistry.ClassifierReleaseManifestV1{
		APIMajor: policy.ClassifierAPIMajorV5, WireSchema: policy.SchemaVersionV5,
		Taxonomy: policyregistry.ClassifierTaxonomy{ClassOrder: classOrder, SHA256: policyregistry.TaxonomyDigest(classOrder)},
		Variants: map[policy.ClassifierVariantID]policyregistry.ClassifierVariantManifest{
			policy.ClassifierVariantPrimary: {ArtifactID: "hmm-v5", ArtifactSHA256: artifactSHA},
		},
	}
	binding := policyregistry.ClassifierRuntimeBinding{
		Environment: policyregistry.EnvironmentProd, ReleaseSHA256: releaseID, WorkerRevision: "router-classifier-worker-00042-abc",
		WorkerURL: testClassifierWorkerURL, WorkerAudience: testClassifierWorkerAudience,
		ImageDigest: "sha256:" + strings.Repeat("6", 64), ConfigurationSHA256: strings.Repeat("5", 64),
	}
	prepared := policyregistry.PreparedSelection{
		Target:                   policyregistry.TargetStable,
		Selection:                policyregistry.ServingSelection{Binding: policyregistry.ObjectRef{SHA256: selectionID}},
		ClassifierReleaseV5:      release,
		ClassifierServiceBinding: &policyregistry.ClassifierServiceBinding{Release: policyregistry.ObjectRef{SHA256: releaseID}, EnabledVariants: []policy.ClassifierVariantID{policy.ClassifierVariantPrimary}},
		ClassifierRuntimeBinding: &policyregistry.ResolvedClassifierRuntimeBinding{Binding: binding},
	}
	endpoints := &classifierWorkerEndpoints{
		staticDestinationEndpoints: staticDestinationEndpoints{err: context.Canceled},
		capabilities: policyregistry.ClassifierServiceAttestation{
			SchemaVersion: policy.SchemaVersionV5, APIMajor: release.APIMajor, WireSchema: release.WireSchema,
			ClassifierSelectionID: selectionID, ClassifierReleaseID: releaseID, ClassOrder: classOrder, TaxonomySHA256: release.Taxonomy.SHA256,
			ClassifierVariants: []policy.ClassifierVariantID{policy.ClassifierVariantPrimary}, SupportsPreview: true,
		},
		worker: policyregistry.ClassifierWorkerAttestation{
			SchemaVersion: policyregistry.ClassifierWorkerAttestationSchemaV1, Status: policyregistry.ClassifierWorkerReady,
			Environment: binding.Environment, ReleaseID: releaseID, WorkerRevision: binding.WorkerRevision,
			ImageDigest: binding.ImageDigest, ConfigurationSHA256: binding.ConfigurationSHA256,
			Variants: map[policy.ClassifierVariantID]policyregistry.ClassifierWorkerVariantAttestation{
				policy.ClassifierVariantPrimary: {ArtifactID: "hmm-v5", ArtifactSHA256: artifactSHA},
			},
		},
	}
	return prepared, endpoints
}

func TestActivationValidatorAttestsV5ReleaseOnRuntimeBoundWorker(t *testing.T) {
	prepared, endpoints := v5PreparedSelection()
	validator := policyregistry.DestinationValidator{Endpoints: endpoints}.BeginActivation()

	err := validator.ValidatePreparedSelection(context.Background(), prepared)
	require.ErrorIs(t, err, context.Canceled, "classifier attestation passes and validation proceeds to the Router worker")
	require.Len(t, endpoints.bindings, 2)
	for _, binding := range endpoints.bindings {
		require.Equal(t, prepared.ClassifierRuntimeBinding.Binding, binding, "attestation dispatches to the binding's URL with its audience")
	}
	require.ErrorIs(t, validator.ValidatePreparedSelection(context.Background(), prepared), context.Canceled)
	require.Len(t, endpoints.bindings, 2, "worker attestations are memoized within one activation")
}

func TestActivationValidatorRejectsWorkerThatDiffersFromRuntimeBinding(t *testing.T) {
	for name, mutate := range map[string]func(*policyregistry.ClassifierWorkerAttestation){
		"revision": func(w *policyregistry.ClassifierWorkerAttestation) {
			w.WorkerRevision = "router-classifier-worker-00041-old"
		},
		"image": func(w *policyregistry.ClassifierWorkerAttestation) {
			w.ImageDigest = "sha256:" + strings.Repeat("7", 64)
		},
		"configuration": func(w *policyregistry.ClassifierWorkerAttestation) { w.ConfigurationSHA256 = strings.Repeat("8", 64) },
		"release":       func(w *policyregistry.ClassifierWorkerAttestation) { w.ReleaseID = strings.Repeat("9", 64) },
		"environment":   func(w *policyregistry.ClassifierWorkerAttestation) { w.Environment = policyregistry.EnvironmentStaging },
		"not ready":     func(w *policyregistry.ClassifierWorkerAttestation) { w.Status = "starting" },
		"variant artifact": func(w *policyregistry.ClassifierWorkerAttestation) {
			w.Variants = map[policy.ClassifierVariantID]policyregistry.ClassifierWorkerVariantAttestation{
				policy.ClassifierVariantPrimary: {ArtifactID: "hmm-v5", ArtifactSHA256: strings.Repeat("e", 64)},
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			prepared, endpoints := v5PreparedSelection()
			mutate(&endpoints.worker)
			err := policyregistry.DestinationValidator{Endpoints: endpoints}.BeginActivation().ValidatePreparedSelection(context.Background(), prepared)
			require.ErrorContains(t, err, "classifier worker")
			require.NotErrorIs(t, err, context.Canceled)
		})
	}
}
