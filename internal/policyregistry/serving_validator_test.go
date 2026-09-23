package policyregistry_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

type staticDestinationEndpoints struct {
	worker     policyregistry.WorkerAttestation
	classifier policyregistry.ClassifierAttestation
	err        error
}

func (e staticDestinationEndpoints) ValidateWorker(context.Context, policyregistry.RevisionBinding, policyregistry.WorkerValidationRequest) (policyregistry.WorkerAttestation, error) {
	return e.worker, e.err
}
func (e staticDestinationEndpoints) AttestClassifier(context.Context, policyregistry.RevisionBinding) (policyregistry.ClassifierAttestation, error) {
	return e.classifier, e.err
}

func validatedEndpoints(prepared policyregistry.PreparedSelection) staticDestinationEndpoints {
	return staticDestinationEndpoints{
		classifier: policyregistry.ClassifierAttestation{Identity: prepared.Candidate.Classifier.Identity, Package: prepared.Candidate.Classifier.Package, Configuration: prepared.Candidate.Classifier.Configuration, AuxiliaryModels: maps.Clone(prepared.Candidate.Classifier.AuxiliaryModels), Revision: prepared.Binding.Classifier.Name, Ready: true},
		worker:     policyregistry.WorkerAttestation{Identity: policyregistry.WorkerIdentity{Target: prepared.Target, Project: prepared.Binding.Project, Region: prepared.Binding.Region, Revision: prepared.Binding.Router.Name, ImageDigest: prepared.Binding.Router.ImageDigest, Configuration: prepared.Binding.Router.Configuration}, Requirements: prepared.Candidate.Requirements, Selection: prepared.Selection, CatalogArms: prepared.Policy.AllArms(), Ready: true},
	}
}

func TestDestinationValidatorRequiresActualCompleteDestinationIdentity(t *testing.T) {
	store, _, set := controllerFixture(t)
	prepared, err := policyregistry.ReadPreparedSelection(context.Background(), store, policyregistry.TargetStable, "", set.Default)
	require.NoError(t, err)
	for _, test := range []struct {
		name     string
		alter    func(*staticDestinationEndpoints)
		expected string
	}{
		{"unknown catalog arm", func(e *staticDestinationEndpoints) { e.worker.CatalogArms = nil }, "absent"},
		{"worker code", func(e *staticDestinationEndpoints) { e.worker.Identity.ImageDigest += "changed" }, "worker readiness"},
		{"worker contract", func(e *staticDestinationEndpoints) { e.worker.Requirements.RuntimeContract = "future" }, "worker readiness"},
		{"worker revision", func(e *staticDestinationEndpoints) { e.worker.Identity.Revision += "changed" }, "worker readiness"},
		{"worker config", func(e *staticDestinationEndpoints) { e.worker.Identity.Configuration = artifactRef("other-config") }, "worker readiness"},
		{"wrong snapshot", func(e *staticDestinationEndpoints) {
			e.worker.Selection.Release = namespaceRef(policyregistry.ServingReleases, "other")
		}, "worker readiness"},
		{"worker unready", func(e *staticDestinationEndpoints) { e.worker.Ready = false }, "worker readiness"},
		{"classifier image", func(e *staticDestinationEndpoints) { e.classifier.Identity.ImageDigest += "changed" }, "classifier readiness"},
		{"classifier revision", func(e *staticDestinationEndpoints) { e.classifier.Revision += "changed" }, "classifier readiness"},
		{"classifier unready", func(e *staticDestinationEndpoints) { e.classifier.Ready = false }, "classifier readiness"},
		{"changed auxiliary", func(e *staticDestinationEndpoints) {
			e.classifier.AuxiliaryModels["escalation"] = artifactRef("other-model")
		}, "auxiliary"},
		{"missing inventory", func(e *staticDestinationEndpoints) { e.classifier.AuxiliaryModels = nil }, "auxiliary"},
		{"changed classifier config", func(e *staticDestinationEndpoints) { e.classifier.Configuration = artifactRef("other-config") }, "configuration"},
		{"private endpoint unavailable", func(e *staticDestinationEndpoints) { e.err = errors.New("HTTP 403") }, "full serving attestation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoints := validatedEndpoints(prepared)
			require.NoError(t, (policyregistry.DestinationValidator{Endpoints: endpoints}).ValidatePreparedSelection(context.Background(), prepared))
			test.alter(&endpoints)
			require.ErrorContains(t, (policyregistry.DestinationValidator{Endpoints: endpoints}).ValidatePreparedSelection(context.Background(), prepared), test.expected)
		})
	}
}

func TestServingPreparationNeverActivatesAndRetryDoesNotRequireHealthyDestination(t *testing.T) {
	store, _, set := controllerFixture(t)
	prepared, err := policyregistry.ReadPreparedSelection(context.Background(), store, policyregistry.TargetStable, "", set.Default)
	require.NoError(t, err)
	controller, err := policyregistry.NewServingController(store, policyregistry.DestinationValidator{Endpoints: validatedEndpoints(prepared)}, func() time.Time { return servingEpoch }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	ref := store.publish(t, policyregistry.ServingProposals, proposal)
	preparation, err := controller.Prepare(context.Background(), ref)
	require.NoError(t, err)
	require.True(t, preparation.Prepared)
	require.Nil(t, preparation.Activation)
	require.Empty(t, store.states)
	activation, err := controller.Activate(context.Background(), ref, "workflow")
	require.NoError(t, err)
	proposal.RequestID = uuid.NewString()
	stale := store.publish(t, policyregistry.ServingProposals, proposal)
	_, err = controller.Prepare(context.Background(), stale)
	require.ErrorIs(t, err, policyregistry.ErrConflict)
	unavailable, err := policyregistry.NewServingController(store, policyregistry.DestinationValidator{Endpoints: staticDestinationEndpoints{err: errors.New("offline")}}, func() time.Time { return servingEpoch }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	preparation, err = unavailable.Prepare(context.Background(), ref)
	require.NoError(t, err)
	require.False(t, preparation.Prepared)
	require.Equal(t, activation.Activation.ID, preparation.Activation.Activation.ID)
	replayed, err := unavailable.Activate(context.Background(), ref, "retry")
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, activation.Activation.ID, replayed.Activation.ID)
}

func TestServingControllerRejectsMissingOrTamperedAuditArtifactsBeforeActivation(t *testing.T) {
	for _, label := range []string{"evidence", "build-attestation", "binding-attestation"} {
		for _, tamper := range []bool{false, true} {
			t.Run(label+map[bool]string{false: "/missing", true: "/tampered"}[tamper], func(t *testing.T) {
				store, controller, set := controllerFixture(t)
				proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
				ref := store.publish(t, policyregistry.ServingProposals, proposal)
				if tamper {
					store.artifacts[artifactRef(label)] = []byte("tampered")
				} else {
					delete(store.artifacts, artifactRef(label))
				}
				_, err := controller.Prepare(context.Background(), ref)
				require.Error(t, err)
				_, err = controller.Activate(context.Background(), ref, "workflow")
				require.Error(t, err)
				require.Empty(t, store.states)
			})
		}
	}
}

func TestServingControllerVerifiesSourceBuildEvidenceForDerivedCompositions(t *testing.T) {
	store, controller, set := controllerFixture(t)
	proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	initial, err := controller.Activate(context.Background(), store.publish(t, policyregistry.ServingProposals, proposal), "workflow")
	require.NoError(t, err)
	source := *store.object(t, policyregistry.ServingReleases, set.Default.Release).(*policyregistry.ServingRelease)
	source.Provenance.BuildAttestation = artifactRef("unpublished-source-attestation")
	proposal = fixtureProposal(t, initial.Snapshot, set, servingEpoch)
	proposal.Scope = policyregistry.ChangeRouter
	proposal.SourceRelease = store.publish(t, policyregistry.ServingReleases, source)
	_, err = controller.Activate(context.Background(), store.publish(t, policyregistry.ServingProposals, proposal), "workflow")
	require.ErrorContains(t, err, "source build attestation")
	require.Equal(t, initial.Snapshot.Generation, store.states[set.Target].Generation)
}
