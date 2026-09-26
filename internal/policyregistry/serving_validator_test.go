package policyregistry_test

import (
	"bytes"
	"context"
	"encoding/json"
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

// countingDestinationEndpoints answers like the real destinations — the classifier attests its own
// revision, the worker derives its attestation from the validation request — and records every call.
type countingDestinationEndpoints struct {
	t               *testing.T
	store           policyregistry.ServingStore
	classifier      policyregistry.ClassifierAttestation
	classifierCalls int
	workerCalls     int
}

func (e *countingDestinationEndpoints) AttestClassifier(_ context.Context, binding policyregistry.RevisionBinding) (policyregistry.ClassifierAttestation, error) {
	e.classifierCalls++
	attestation := e.classifier
	attestation.Revision = binding.Name
	return attestation, nil
}

func (e *countingDestinationEndpoints) ValidateWorker(ctx context.Context, _ policyregistry.RevisionBinding, request policyregistry.WorkerValidationRequest) (policyregistry.WorkerAttestation, error) {
	e.workerCalls++
	prepared, err := policyregistry.ReadPreparedSelection(ctx, e.store, request.Target, request.ProfileKey, request.Selection)
	require.NoError(e.t, err)
	return validatedEndpoints(prepared).worker, nil
}

func preparedLanes(t *testing.T, store policyregistry.ServingStore, set policyregistry.SelectionSet) []policyregistry.PreparedSelection {
	t.Helper()
	lanes := make([]policyregistry.PreparedSelection, 0, len(set.Profiles)+1)
	prepared, err := policyregistry.ReadPreparedSelection(context.Background(), store, set.Target, "", set.Default)
	require.NoError(t, err)
	lanes = append(lanes, prepared)
	for key, selection := range set.Profiles {
		profile, err := policyregistry.ReadPreparedSelection(context.Background(), store, set.Target, key, selection)
		require.NoError(t, err)
		lanes = append(lanes, profile)
	}
	return lanes
}

func TestActivationMemoizesDestinationAttestationsPerRevisionBinding(t *testing.T) {
	store, _, set := controllerFixture(t)
	base := store.object(t, policyregistry.ServingReleases, set.Default.Release).(*policyregistry.ServingRelease)
	set.Profiles[profileKeyOne] = registerProfileFixture(t, store, set.Default, profileKeyOne, base.Policy)
	set.Profiles[profileKeyTwo] = registerProfileFixture(t, store, set.Default, profileKeyTwo, base.Policy)
	store.publish(t, policyregistry.ServingSelectionSets, set)
	lanes := preparedLanes(t, store, set)
	require.Len(t, lanes, 3)
	for _, lane := range lanes[1:] {
		require.Equal(t, lanes[0].Binding.Classifier, lane.Binding.Classifier)
		require.Equal(t, lanes[0].Binding.Router, lane.Binding.Router)
	}
	endpoints := &countingDestinationEndpoints{t: t, store: store, classifier: validatedEndpoints(lanes[0]).classifier}

	unmemoized := policyregistry.DestinationValidator{Endpoints: endpoints}
	for _, lane := range lanes {
		require.NoError(t, unmemoized.ValidatePreparedSelection(context.Background(), lane))
	}
	require.Equal(t, 3, endpoints.classifierCalls, "without memoization every lane re-attests the shared classifier revision")
	require.Equal(t, 3, endpoints.workerCalls)

	endpoints.classifierCalls, endpoints.workerCalls = 0, 0
	var audit bytes.Buffer
	controller, err := policyregistry.NewServingController(store, unmemoized, func() time.Time { return servingEpoch }, slog.New(slog.NewJSONHandler(&audit, nil)))
	require.NoError(t, err)
	proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	preparation, err := controller.Prepare(context.Background(), store.publish(t, policyregistry.ServingProposals, proposal))
	require.NoError(t, err)
	require.True(t, preparation.Prepared)
	require.Equal(t, 1, endpoints.classifierCalls, "lanes sharing one classifier revision are attested once per activation")
	require.Equal(t, 3, endpoints.workerCalls, "worker attestations depend on the validation request, so each lane keeps its own")

	entry := destinationValidationEntry(t, &audit)
	require.Equal(t, "attested", entry["destination_validation_outcome"])
	require.Equal(t, float64(3), entry["destination_validation_lanes"])
	require.Equal(t, float64(4), entry["destination_validation_http_calls"])
	require.Equal(t, float64(2), entry["destination_validation_cache_hits"])

	// A second activation re-attests: the memo never outlives one validation.
	endpoints.classifierCalls = 0
	proposal.RequestID = uuid.NewString()
	_, err = controller.Prepare(context.Background(), store.publish(t, policyregistry.ServingProposals, proposal))
	require.NoError(t, err)
	require.Equal(t, 1, endpoints.classifierCalls)
}

func TestActivationAttestsEveryDistinctRevisionBinding(t *testing.T) {
	store, _, set := controllerFixture(t)
	lanes := preparedLanes(t, store, set)
	require.Len(t, lanes, 1)
	binding := *store.object(t, policyregistry.ServingBindings, set.Default.Binding).(*policyregistry.DeploymentBinding)
	binding.Router.Name = "worker-0002"
	binding.Router.URL = "https://worker-0002.example"
	binding.Classifier.Name = "classifier-0002"
	binding.Classifier.URL = "https://classifier-0002.example"
	other, err := policyregistry.ReadPreparedSelection(context.Background(), store, set.Target, "", policyregistry.ServingSelection{Release: set.Default.Release, Binding: store.publish(t, policyregistry.ServingBindings, binding)})
	require.NoError(t, err)
	require.NotEqual(t, lanes[0].Binding.Classifier, other.Binding.Classifier)

	endpoints := &countingDestinationEndpoints{t: t, store: store, classifier: validatedEndpoints(lanes[0]).classifier}
	validator := (policyregistry.DestinationValidator{Endpoints: endpoints}).BeginActivation()
	require.NoError(t, validator.ValidatePreparedSelection(context.Background(), lanes[0]))
	require.NoError(t, validator.ValidatePreparedSelection(context.Background(), other))
	require.Equal(t, 2, endpoints.classifierCalls, "a different classifier revision must never reuse another revision's attestation")
	require.Equal(t, 2, endpoints.workerCalls)
	require.Equal(t, policyregistry.ValidationStats{HTTPCalls: 4}, validator.Stats())
}

func TestActivationMemoizesDestinationFailuresFailClosed(t *testing.T) {
	store, _, set := controllerFixture(t)
	lanes := preparedLanes(t, store, set)
	failing := &flakyDestinationEndpoints{}
	validator := (policyregistry.DestinationValidator{Endpoints: failing}).BeginActivation()
	require.ErrorContains(t, validator.ValidatePreparedSelection(context.Background(), lanes[0]), "full serving attestation")
	require.ErrorContains(t, validator.ValidatePreparedSelection(context.Background(), lanes[0]), "full serving attestation")
	require.Equal(t, 1, failing.calls, "a recorded attestation failure is replayed instead of retried")
	require.Equal(t, policyregistry.ValidationStats{HTTPCalls: 1, CacheHits: 1}, validator.Stats())
}

func TestBlockedActivationLogsPartialDestinationValidation(t *testing.T) {
	store, _, set := controllerFixture(t)
	base := store.object(t, policyregistry.ServingReleases, set.Default.Release).(*policyregistry.ServingRelease)
	set.Profiles[profileKeyOne] = registerProfileFixture(t, store, set.Default, profileKeyOne, base.Policy)
	store.publish(t, policyregistry.ServingSelectionSets, set)
	var audit bytes.Buffer
	validator := policyregistry.DestinationValidator{Endpoints: &flakyDestinationEndpoints{}}
	controller, err := policyregistry.NewServingController(store, validator, func() time.Time { return servingEpoch }, slog.New(slog.NewJSONHandler(&audit, nil)))
	require.NoError(t, err)
	proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	_, err = controller.Prepare(context.Background(), store.publish(t, policyregistry.ServingProposals, proposal))
	require.Error(t, err)

	entry := destinationValidationEntry(t, &audit)
	require.Equal(t, "blocked", entry["destination_validation_outcome"])
	require.Equal(t, float64(1), entry["destination_validation_lanes"], "a blocked validation stops on the failing lane and reports partial counts")
}

// flakyDestinationEndpoints fails the first classifier attestation and would succeed afterwards.
type flakyDestinationEndpoints struct {
	calls int
}

func (e *flakyDestinationEndpoints) AttestClassifier(context.Context, policyregistry.RevisionBinding) (policyregistry.ClassifierAttestation, error) {
	e.calls++
	if e.calls == 1 {
		return policyregistry.ClassifierAttestation{}, errors.New("HTTP 503")
	}
	return policyregistry.ClassifierAttestation{Ready: true}, nil
}

func (e *flakyDestinationEndpoints) ValidateWorker(context.Context, policyregistry.RevisionBinding, policyregistry.WorkerValidationRequest) (policyregistry.WorkerAttestation, error) {
	return policyregistry.WorkerAttestation{}, errors.New("unexpected worker validation")
}

// destinationValidationEntry returns the per-activation destination validation audit line.
func destinationValidationEntry(t *testing.T, audit *bytes.Buffer) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(audit)
	for {
		var entry map[string]any
		require.NoError(t, decoder.Decode(&entry))
		if _, exists := entry["destination_validation_lanes"]; exists {
			return entry
		}
	}
}
