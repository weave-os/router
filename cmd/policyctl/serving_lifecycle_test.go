package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/rosterdata"
)

type cliServingRegistry struct {
	objects   map[policyregistry.ObjectRef][]byte
	policy    *rosterdata.Roster
	state     policyregistry.ServingStateSnapshot
	writes    int
	casError  error
	artifacts map[policyregistry.ObjectRef][]byte
}

func (r *cliServingRegistry) VerifyServingArtifact(_ context.Context, ref policyregistry.ObjectRef) error {
	payload, exists := r.artifacts[ref]
	if !exists {
		return policyregistry.ErrNotFound
	}
	if policyregistry.Digest(payload) != ref.SHA256 {
		return errors.New("artifact digest mismatch")
	}
	return nil
}

func (r *cliServingRegistry) RootURI() string { return defaultRegistryURI }
func (r *cliServingRegistry) Close() error    { return nil }
func (r *cliServingRegistry) ReadServingObject(_ context.Context, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	payload, ok := r.objects[ref]
	if !ok {
		return nil, nil, policyregistry.ErrNotFound
	}
	manifest, err := policyregistry.DecodeServingManifest(payload, r.RootURI(), kind)
	if err != nil {
		return nil, nil, err
	}
	return manifest, payload, nil
}
func (r *cliServingRegistry) ReadServingPolicy(context.Context, policyregistry.ObjectRef) (*rosterdata.Roster, error) {
	return r.policy, nil
}
func (r *cliServingRegistry) ReadServingState(context.Context, policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	if r.state.Generation == 0 {
		return r.state, policyregistry.ErrNotFound
	}
	return r.state, nil
}
func (r *cliServingRegistry) CompareAndSwapServingState(_ context.Context, state policyregistry.ServingControlState, generation int64) (policyregistry.ServingStateSnapshot, error) {
	if r.casError != nil {
		return policyregistry.ServingStateSnapshot{}, r.casError
	}
	if generation != r.state.Generation {
		return policyregistry.ServingStateSnapshot{}, policyregistry.ErrConflict
	}
	r.writes++
	r.state = policyregistry.ServingStateSnapshot{State: state, Generation: generation + 1}
	return r.state, nil
}

// PublishServingManifest also accepts v1 kinds so fixtures can seed the legacy objects that a
// real registry only reads.
func (r *cliServingRegistry) PublishServingManifest(_ context.Context, kind policyregistry.ServingKind, payload []byte) (policyregistry.ObjectRef, error) {
	if _, err := policyregistry.DecodeServingManifest(payload, r.RootURI(), kind); err != nil {
		return policyregistry.ObjectRef{}, err
	}
	digest := policyregistry.Digest(payload)
	ref := policyregistry.ObjectRef{URI: r.RootURI() + "/router_serving/v1/" + string(kind) + "/sha256/" + digest + ".json", SHA256: digest, Generation: 1}
	if policyregistry.ValidatePublishableServingKind(kind) == nil {
		ref.URI = r.RootURI() + "/artifacts/" + digest + ".json"
	}
	r.objects[ref] = payload
	return ref, nil
}
func (r *cliServingRegistry) ServingRef(_ context.Context, kind policyregistry.ServingKind, digest string) (policyregistry.ObjectRef, error) {
	for ref := range r.objects {
		if ref.SHA256 == digest && policyregistry.ValidateServingRef(ref, r.RootURI(), kind) == nil {
			return ref, nil
		}
	}
	return policyregistry.ObjectRef{}, policyregistry.ErrNotFound
}

type cliDestinationEndpoints struct {
	registry *cliServingRegistry
	bundle   policyregistry.ClassifierBundle
	err      error
}

func (e *cliDestinationEndpoints) AttestClassifier(_ context.Context, revision policyregistry.RevisionBinding) (policyregistry.ClassifierAttestation, error) {
	return policyregistry.ClassifierAttestation{Ready: true, Revision: revision.Name, Identity: e.bundle.Identity, Package: e.bundle.Package, AuxiliaryModels: e.bundle.AuxiliaryModels, Configuration: e.bundle.Configuration}, e.err
}
func (e *cliDestinationEndpoints) ValidateWorker(ctx context.Context, _ policyregistry.RevisionBinding, request policyregistry.WorkerValidationRequest) (policyregistry.WorkerAttestation, error) {
	prepared, err := policyregistry.ReadPreparedSelection(ctx, e.registry, request.Target, request.ProfileKey, request.Selection)
	if err != nil {
		return policyregistry.WorkerAttestation{}, err
	}
	return policyregistry.WorkerAttestation{Ready: true, Selection: request.Selection, Requirements: prepared.Candidate.Requirements, CatalogArms: e.registry.policy.AllArms(), Identity: policyregistry.WorkerIdentity{Target: request.Target, Project: prepared.Binding.Project, Region: prepared.Binding.Region, Revision: prepared.Binding.Router.Name, ImageDigest: prepared.Binding.Router.ImageDigest, Configuration: prepared.Binding.Router.Configuration}}, e.err
}

func cliPublish(t *testing.T, registry *cliServingRegistry, kind policyregistry.ServingKind, manifest policyregistry.ServingManifest) policyregistry.ObjectRef {
	t.Helper()
	encoded, err := policyregistry.CanonicalBytes(manifest)
	require.NoError(t, err)
	ref, err := registry.PublishServingManifest(context.Background(), kind, encoded)
	require.NoError(t, err)
	return ref
}

func cliProposalFile(t *testing.T, ref policyregistry.ObjectRef) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proposal.json")
	encoded, err := json.Marshal(ref)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, encoded, 0o600))
	return path
}

func cliServingFixture(t *testing.T) (*cliServingRegistry, *cliDestinationEndpoints, policyregistry.DeploymentProposal) {
	t.Helper()
	policy, err := rosterdata.ParseValidated([]byte(`{"schema_version":"hmm_go_selection_policy_v1","class_order":["low"],"ranking":{"alpha":{"low":0.4},"alpha_min":{"low":0.1},"alpha_max":{"low":0.8},"quality_bias_neutral":0.7,"wii_score_version":"wii-v1","wii_normalization_sha256":"wii","wpi_score_version":"wpi-v1","wpi_normalization_sha256":"wpi"},"preferences":{"preferred_model_bonus":0.5,"subscription_bonus":0.35},"clusters":{"low":{"complexity_label":"low","arms":["openai/gpt-5.6-sol"],"cost_ref_usd":1,"latency_ref_ms":1,"arm_scores":{"openai/gpt-5.6-sol":1},"arm_indices":{"openai/gpt-5.6-sol":{"wii_v1":80,"wpi_v1":40}}}}}`))
	require.NoError(t, err)
	registry := &cliServingRegistry{objects: make(map[policyregistry.ObjectRef][]byte), policy: policy}
	artifact := policyregistry.ObjectRef{URI: defaultRegistryURI + "/fixture/artifact", SHA256: policyregistry.Digest([]byte("artifact")), Generation: 1}
	registry.artifacts = map[policyregistry.ObjectRef][]byte{artifact: []byte("artifact")}
	image := "sha256:" + strings.Repeat("b", 64)
	bundle := policyregistry.ClassifierBundle{SchemaVersion: policyregistry.ServingClassifierV1, Identity: policyregistry.ClassifierIdentity{ArtifactID: "fixture", PackageSHA256: artifact.SHA256, ImageDigest: image, WireSchema: policyregistry.ClassifierWireSchemaV4, ClassOrder: policy.ClassOrder, TaxonomySHA256: policyregistry.TaxonomyDigest(policy.ClassOrder)}, Package: artifact, Configuration: artifact, AuxiliaryModels: map[string]policyregistry.ObjectRef{}}
	bundleRef := cliPublish(t, registry, policyregistry.ServingClassifiers, bundle)
	policyBytes, err := rosterdata.CanonicalBytes(policy)
	require.NoError(t, err)
	policyDigest := policyregistry.Digest(policyBytes)
	release := policyregistry.ServingRelease{SchemaVersion: policyregistry.ServingReleaseV1, RouterImageDigest: image, Policy: policyregistry.PolicyObject{URI: defaultRegistryURI + "/router_policy/v1/policies/sha256/" + policyDigest + ".json", SHA256: policyDigest, Generation: 1, SchemaVersion: policy.SchemaVersion}, Classifier: bundleRef, Requirements: policyregistry.ServingRequirements{RuntimeContract: policyregistry.ManagedRuntimeContractV1, PolicySchema: policy.SchemaVersion, ClassifierWireSchema: bundle.Identity.WireSchema, TaxonomySHA256: bundle.Identity.TaxonomySHA256}, Provenance: policyregistry.ServingProvenance{RouterRevision: strings.Repeat("c", 40), WeaveRevision: strings.Repeat("d", 40), BuildAttestation: artifact}}
	releaseRef := cliPublish(t, registry, policyregistry.ServingReleases, release)
	binding := policyregistry.DeploymentBinding{SchemaVersion: policyregistry.ServingBindingV1, Target: policyregistry.TargetStable, Project: "test-project", Region: "test-region", Release: releaseRef, Router: policyregistry.RevisionBinding{Name: "worker-1", URL: "https://worker-1.example", Audience: "https://worker.example", ImageDigest: image, Configuration: artifact}, Classifier: policyregistry.RevisionBinding{Name: "classifier-1", URL: "https://classifier-1.example", Audience: "https://classifier.example", ImageDigest: image, Configuration: artifact}, ClassifierBundleSHA256: bundleRef.SHA256, Attestation: artifact}
	bindingRef := cliPublish(t, registry, policyregistry.ServingBindings, binding)
	setRef := cliPublish(t, registry, policyregistry.ServingSelectionSets, policyregistry.SelectionSet{SchemaVersion: policyregistry.ServingSelectionSetV1, Target: binding.Target, Default: policyregistry.ServingSelection{Release: releaseRef, Binding: bindingRef}, Profiles: map[string]policyregistry.ServingSelection{}})
	proposal := policyregistry.DeploymentProposal{SchemaVersion: policyregistry.ServingProposalV1, Target: binding.Target, SelectionSet: setRef, SourceRelease: releaseRef, Scope: policyregistry.ChangeFull, Actor: "original-operator", Reason: "fixture activation", RequestID: "run-1:lane-0", CreatedAt: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), Evidence: []policyregistry.ObjectRef{artifact}, WithdrawActivations: []string{}}
	return registry, &cliDestinationEndpoints{registry: registry, bundle: bundle}, proposal
}

func TestServingCLIProposalPreparationActivationRollbackAndReconciliation(t *testing.T) {
	registry, endpoints, proposal := cliServingFixture(t)
	ref := cliPublish(t, registry, policyregistry.ServingProposals, proposal)
	path := cliProposalFile(t, ref)
	var output any
	var stderr bytes.Buffer
	env := map[string]string{"WORKFLOW_ACTOR": "github-actions:weave-os/weave:4242:1", "GITHUB_ACTOR": "ci-bot", "GITHUB_RUN_ID": "4242", "USER": "local-operator"}
	dependencies := servingDependencies{openRegistry: func(context.Context, string) (servingRegistry, error) { return registry, nil }, endpoints: func() (policyregistry.DestinationEndpoints, error) { return endpoints, nil }, writeOutput: func(value any) error { output = value; return nil }, clock: func() time.Time { return proposal.CreatedAt }, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stderr: &stderr, getenv: func(key string) string { return env[key] }}
	ctx := context.Background()
	require.NoError(t, runServingWith(ctx, []string{string(commandResolve), "--proposal-sha256", ref.SHA256}, dependencies))
	require.Equal(t, ref, output)
	require.NoError(t, runServingWith(ctx, []string{string(commandPrepare), "--proposal", path}, dependencies))
	require.Zero(t, registry.writes)
	require.Empty(t, stderr.String())
	args := []string{string(commandActivate), "--proposal", path, "--approved-proposal", strings.Repeat("f", 64), "--workflow-actor", "workflow-service", "--validation-origin", "https://ignored.example"}
	require.NoError(t, runServingWith(ctx, args, dependencies))
	first := output.(policyregistry.ActivationResult)
	require.Equal(t, "original-operator", first.Activation.Actor)
	require.Equal(t, "github-actions:weave-os/weave:4242:1", first.Activation.WorkflowActor, "the Cloud Run workflow identity takes precedence over the local environment and ignored flag")
	for _, name := range deprecatedServingFlags {
		require.Contains(t, stderr.String(), "--"+name+" is deprecated")
	}
	require.Equal(t, 3, strings.Count(stderr.String(), "\n"), "one warning line per ignored flag")
	delete(env, "WORKFLOW_ACTOR")
	require.Equal(t, "ci-bot@run:4242", workflowActorFor(dependencies.getenv, proposal.View()))
	delete(env, "GITHUB_RUN_ID")
	rollback := proposal
	rollback.ExpectedGeneration = first.Snapshot.Generation
	rollback.PreviousSelectionSet = &proposal.SelectionSet
	rollback.WithdrawActivations = []string{first.Activation.ID}
	rollbackRef := cliPublish(t, registry, policyregistry.ServingProposals, rollback)
	rollbackPath := cliProposalFile(t, rollbackRef)
	require.NoError(t, runServingWith(ctx, []string{string(commandRollback), "--proposal", rollbackPath}, dependencies))
	second := output.(policyregistry.ActivationResult)
	require.NotEqual(t, first.Activation.ID, second.Activation.ID)
	require.Equal(t, "local-operator", second.Activation.WorkflowActor, "$USER is the fallback when no GitHub run is present")
	require.Equal(t, first.Activation.RequestID, second.Activation.RequestID, "a shared request ID is audit metadata; the distinct proposal ref makes this a second activation")
	require.Equal(t, second.Activation.ID, second.Snapshot.State.Activations[first.Activation.ID].ReplacementID)
	endpoints.err = errors.New("destination offline")
	require.NoError(t, runServingWith(ctx, []string{string(commandPrepare), "--proposal", path}, dependencies))
	reconciled := output.(policyregistry.PreparationResult)
	require.False(t, reconciled.Prepared)
	require.Equal(t, policyregistry.ActivationSuperseded, reconciled.Activation.Outcome)
	require.NoError(t, runServingWith(ctx, args, dependencies))
	require.Equal(t, policyregistry.ActivationSuperseded, output.(policyregistry.ActivationResult).Outcome)
	require.NoError(t, runServingWith(ctx, []string{string(commandStatus), "--proposal", path}, dependencies))
	require.Equal(t, first.Activation.ID, output.(policyregistry.ActivationResult).Activation.ID)
	require.NoError(t, runServingWith(ctx, []string{string(commandStatus), "--proposal", rollbackPath}, dependencies))
	require.Equal(t, second.Activation.ID, output.(policyregistry.ActivationResult).Activation.ID, "status reconciles by proposal ref, not request ID")
	require.Equal(t, 2, registry.writes)
}

func TestServingCLIStaleProposalsAndCommittedOutputFailure(t *testing.T) {
	registry, endpoints, proposal := cliServingFixture(t)
	ref := cliPublish(t, registry, policyregistry.ServingProposals, proposal)
	path := cliProposalFile(t, ref)
	failOutput := true
	var output any
	dependencies := servingDependencies{openRegistry: func(context.Context, string) (servingRegistry, error) { return registry, nil }, endpoints: func() (policyregistry.DestinationEndpoints, error) { return endpoints, nil }, writeOutput: func(value any) error {
		if failOutput {
			return errors.New("broken output pipe")
		}
		output = value
		return nil
	}, clock: func() time.Time { return proposal.CreatedAt }, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()
	args := []string{string(commandActivate), "--proposal", path}
	require.ErrorContains(t, runServingWith(ctx, args, dependencies), "activated; output observation degraded")
	require.Equal(t, 1, registry.writes)
	failOutput = false
	require.NoError(t, runServingWith(ctx, []string{string(commandStatus), "--proposal", path}, dependencies))
	reconciled := output.(policyregistry.ActivationResult)
	require.True(t, reconciled.Replayed)
	require.Equal(t, proposal.Actor, reconciled.Activation.WorkflowActor, "without a GitHub run or $USER the proposal operator is the executing identity")
	require.Equal(t, policyregistry.ActivationCurrent, reconciled.Outcome)
	require.Equal(t, 1, registry.writes)
	proposal.RequestID = "run-2:lane-0"
	stale := cliPublish(t, registry, policyregistry.ServingProposals, proposal)
	require.ErrorIs(t, runServingWith(ctx, []string{string(commandPrepare), "--proposal", cliProposalFile(t, stale)}, dependencies), policyregistry.ErrConflict)
	require.Equal(t, 1, registry.writes)
}

func TestServingCLIValidateAcceptsAnyValidEncoding(t *testing.T) {
	_, _, proposal := cliServingFixture(t)
	canonical, err := policyregistry.CanonicalBytes(proposal)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(canonical, &decoded))
	drifted, err := json.Marshal(decoded)
	require.NoError(t, err)
	drifted = append(drifted, '\n')
	path := filepath.Join(t.TempDir(), "proposal.json")
	require.NoError(t, os.WriteFile(path, drifted, 0o600))

	var output any
	dependencies := servingDependencies{
		openRegistry: func(context.Context, string) (servingRegistry, error) {
			return nil, errors.New("registry must not be opened for stored validation")
		},
		writeOutput: func(value any) error { output = value; return nil },
		clock:       func() time.Time { return proposal.CreatedAt },
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx := context.Background()
	args := []string{string(commandValidate), "--kind", string(policyregistry.ServingProposal), "--manifest", path}
	require.NoError(t, runServingWith(ctx, args, dependencies))
	encoded, err := json.Marshal(output)
	require.NoError(t, err)
	require.Contains(t, string(encoded), policyregistry.Digest(bytes.TrimSpace(drifted)), "publish-shaped validation reports the digest of the trimmed bytes it would publish")
	require.NoError(t, runServingWith(ctx, append(args, "--stored"), dependencies))
	encoded, err = json.Marshal(output)
	require.NoError(t, err)
	require.Contains(t, string(encoded), policyregistry.Digest(drifted), "stored validation reports the digest of the exact bytes")
	require.ErrorContains(t, runServingWith(ctx, []string{string(commandPublish), "--kind", string(policyregistry.ServingProposal), "--manifest", path, "--stored"}, dependencies), "--stored only applies to serving validate")
}

func TestServingCLIKindAcceptsOnlyV2KindsWithFoldingGuidance(t *testing.T) {
	registry, _, proposal := cliServingFixture(t)
	payload, err := policyregistry.CanonicalBytes(proposal)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "manifest.json")
	require.NoError(t, os.WriteFile(path, payload, 0o600))
	opened := 0
	var output any
	dependencies := servingDependencies{
		openRegistry: func(context.Context, string) (servingRegistry, error) { opened++; return registry, nil },
		writeOutput:  func(value any) error { output = value; return nil },
		clock:        func() time.Time { return proposal.CreatedAt },
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx := context.Background()
	for kind, folded := range map[string]string{"releases": "candidate", "classifiers": "candidate", "bindings": "selection_set", "profiles": "selection_set", "selection_sets": "selection_set", "proposals": "proposal"} {
		for _, command := range []commandName{commandValidate, commandPublish} {
			err := runServingWith(ctx, []string{string(command), "--kind", kind, "--manifest", path}, dependencies)
			require.ErrorContains(t, err, "folded into \""+folded+"\"", "%s --kind %s", command, kind)
		}
	}
	require.ErrorContains(t, runServingWith(ctx, []string{string(commandPublish), "--kind", "lanes", "--manifest", path}, dependencies), "unsupported serving object kind")
	require.Zero(t, opened, "kind guidance is offered before the registry is opened")

	v2 := policyregistry.DeploymentProposalV2{SchemaVersion: policyregistry.ServingProposalV2, Target: proposal.Target, SelectionSet: proposal.SelectionSet, SourceCandidate: proposal.SourceRelease, Scope: proposal.Scope, Actor: proposal.Actor, Reason: proposal.Reason, RequestID: proposal.RequestID, CreatedAt: proposal.CreatedAt, Evidence: proposal.Evidence, WithdrawActivations: []string{}}
	v2Payload, err := policyregistry.CanonicalBytes(v2)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, v2Payload, 0o600))
	require.NoError(t, runServingWith(ctx, []string{string(commandPublish), "--kind", string(policyregistry.ServingProposal), "--manifest", path}, dependencies))
	ref := output.(policyregistry.ObjectRef)
	require.Equal(t, defaultRegistryURI+"/artifacts/"+policyregistry.Digest(v2Payload)+".json", ref.URI)
	manifest, _, err := registry.ReadServingObject(ctx, policyregistry.ServingProposal, ref)
	require.NoError(t, err)
	require.Equal(t, &v2, manifest)
}

func TestServingCLILifecycleAcceptsV2ProposalsOverV1History(t *testing.T) {
	registry, endpoints, proposal := cliServingFixture(t)
	var output any
	env := map[string]string{"GITHUB_ACTOR": "ci-bot", "GITHUB_RUN_ID": "4243"}
	dependencies := servingDependencies{openRegistry: func(context.Context, string) (servingRegistry, error) { return registry, nil }, endpoints: func() (policyregistry.DestinationEndpoints, error) { return endpoints, nil }, writeOutput: func(value any) error { output = value; return nil }, clock: func() time.Time { return proposal.CreatedAt }, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), getenv: func(key string) string { return env[key] }}
	ctx := context.Background()
	require.NoError(t, runServingWith(ctx, []string{string(commandActivate), "--proposal", cliProposalFile(t, cliPublish(t, registry, policyregistry.ServingProposals, proposal))}, dependencies))
	require.EqualValues(t, 1, output.(policyregistry.ActivationResult).Snapshot.Generation)

	v2 := policyregistry.DeploymentProposalV2{SchemaVersion: policyregistry.ServingProposalV2, Target: proposal.Target, PreviousSelectionSet: &proposal.SelectionSet, SelectionSet: proposal.SelectionSet, SourceCandidate: proposal.SourceRelease, Scope: policyregistry.ChangeFull, Actor: "v2-operator", Reason: "v2 proposal on v1 history", RequestID: "run-2:lane-0", CreatedAt: proposal.CreatedAt, Evidence: proposal.Evidence, WithdrawActivations: []string{}}
	ref := cliPublish(t, registry, policyregistry.ServingProposal, v2)
	require.Contains(t, ref.URI, "/artifacts/")
	path := cliProposalFile(t, ref)
	require.NoError(t, runServingWith(ctx, []string{string(commandResolve), "--proposal-sha256", ref.SHA256}, dependencies))
	require.Equal(t, ref, output)
	require.NoError(t, runServingWith(ctx, []string{string(commandPrepare), "--proposal", path}, dependencies))
	require.True(t, output.(policyregistry.PreparationResult).Prepared)
	require.NoError(t, runServingWith(ctx, []string{string(commandActivate), "--proposal", path}, dependencies))
	activated := output.(policyregistry.ActivationResult)
	require.Equal(t, "v2-operator", activated.Activation.Actor)
	require.Equal(t, "ci-bot@run:4243", activated.Activation.WorkflowActor)
	require.Equal(t, ref, activated.Activation.Proposal)
	require.EqualValues(t, 2, activated.Snapshot.Generation)
	require.NoError(t, runServingWith(ctx, []string{string(commandStatus), "--proposal", path}, dependencies))
	require.Equal(t, activated.Activation.ID, output.(policyregistry.ActivationResult).Activation.ID)

	rollback := v2
	rollback.RequestID = "run-3:lane-0"
	rollback.WithdrawActivations = []string{activated.Activation.ID}
	rollbackPath := cliProposalFile(t, cliPublish(t, registry, policyregistry.ServingProposal, rollback))
	require.NoError(t, runServingWith(ctx, []string{string(commandRollback), "--proposal", rollbackPath}, dependencies))
	rolled := output.(policyregistry.ActivationResult)
	require.NotEqual(t, activated.Activation.ID, rolled.Activation.ID)
	require.Equal(t, rolled.Activation.ID, rolled.Snapshot.State.Activations[activated.Activation.ID].ReplacementID)
	require.EqualValues(t, 3, rolled.Snapshot.Generation)

	proposal.RequestID = "run-4:lane-0"
	stale := cliProposalFile(t, cliPublish(t, registry, policyregistry.ServingProposals, proposal))
	require.ErrorIs(t, runServingWith(ctx, []string{string(commandPrepare), "--proposal", stale}, dependencies), policyregistry.ErrConflict, "v1 proposals still carry expected_generation and freeze against it")
	require.Equal(t, 3, registry.writes)
}
