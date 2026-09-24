package main

import (
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

func cliManifestFile(t *testing.T, payload []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	require.NoError(t, os.WriteFile(path, payload, 0o600))
	return path
}

func cliDependencies(registry *cliServingRegistry, endpoints *cliDestinationEndpoints, output *any, env map[string]string) servingDependencies {
	return servingDependencies{
		openRegistry: func(context.Context, string) (servingRegistry, error) { return registry, nil },
		endpoints:    func() (policyregistry.DestinationEndpoints, error) { return endpoints, nil },
		writeOutput:  func(value any) error { *output = value; return nil },
		clock:        func() time.Time { return time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC) },
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		getenv:       func(key string) string { return env[key] },
	}
}

// cliV2Proposal composes a v2 proposal over the fixture's v1 candidate and selection set.
func cliV2Proposal(proposal policyregistry.DeploymentProposal, requestID string) policyregistry.DeploymentProposalV2 {
	return policyregistry.DeploymentProposalV2{SchemaVersion: policyregistry.ServingProposalV2, Target: proposal.Target, SelectionSet: proposal.SelectionSet, SourceCandidate: proposal.SourceRelease, Scope: policyregistry.ChangeFull, Actor: "v2-operator", Reason: "v2 proposal", RequestID: requestID, CreatedAt: proposal.CreatedAt, Evidence: proposal.Evidence, WithdrawActivations: []string{}}
}

func TestServingCLIPublishDryRunReportsStoredDigestWithoutWriting(t *testing.T) {
	registry, _, proposal := cliServingFixture(t)
	canonical, err := policyregistry.CanonicalBytes(cliV2Proposal(proposal, "run-1:lane-0"))
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(canonical, &decoded))
	drifted, err := json.Marshal(decoded)
	require.NoError(t, err)
	path := cliManifestFile(t, append(drifted, '\n'))
	opened := 0
	var output any
	dependencies := cliDependencies(registry, nil, &output, nil)
	dependencies.openRegistry = func(context.Context, string) (servingRegistry, error) { opened++; return registry, nil }
	ctx := context.Background()
	args := []string{string(commandPublish), "--kind", string(policyregistry.ServingProposal), "--manifest", path}
	seeded := len(registry.objects)

	require.NoError(t, runServingWith(ctx, append(args, "--dry-run"), dependencies))
	encoded, err := json.Marshal(output)
	require.NoError(t, err)
	require.JSONEq(t, `{"kind":"proposal","sha256":"`+policyregistry.Digest(drifted)+`"}`, string(encoded), "the dry run reports the digest of the bytes a publish would store")
	require.Zero(t, opened, "a dry run never opens the registry")
	require.Len(t, registry.objects, seeded, "a dry run writes nothing")

	require.NoError(t, runServingWith(ctx, args, dependencies))
	ref := output.(policyregistry.ObjectRef)
	require.Equal(t, policyregistry.Digest(drifted), ref.SHA256, "publish stores exactly the bytes the dry run digested")
	require.Equal(t, defaultRegistryURI+"/artifacts/"+ref.SHA256+".json", ref.URI)
	require.Equal(t, 1, opened)
	require.Equal(t, drifted, registry.objects[ref])

	require.ErrorContains(t, runServingWith(ctx, []string{string(commandPublish), "--kind", string(policyregistry.ServingProposal), "--manifest", path, "--stored"}, dependencies), "flag provided but not defined: -stored")
	require.ErrorContains(t, runServingWith(ctx, []string{string(commandPublish), "--dry-run", "--manifest", path}, dependencies), "requires --kind and --manifest")
}

func TestServingCLIPublishDryRunAppliesEveryPublishRejection(t *testing.T) {
	registry, _, proposal := cliServingFixture(t)
	v1Payload, err := policyregistry.CanonicalBytes(proposal)
	require.NoError(t, err)
	path := cliManifestFile(t, v1Payload)
	opened := 0
	var output any
	dependencies := cliDependencies(registry, nil, &output, nil)
	dependencies.openRegistry = func(context.Context, string) (servingRegistry, error) { opened++; return registry, nil }
	ctx := context.Background()
	for _, dryRun := range []bool{true, false} {
		publish := func(kind string) error {
			args := []string{string(commandPublish), "--manifest", path, "--kind", kind}
			if dryRun {
				args = append(args, "--dry-run")
			}
			return runServingWith(ctx, args, dependencies)
		}
		for kind, folded := range map[string]string{"releases": "candidate", "classifiers": "candidate", "bindings": "selection_set", "profiles": "selection_set", "selection_sets": "selection_set", "proposals": "proposal"} {
			require.ErrorContains(t, publish(kind), "folded into \""+folded+"\"", "dry_run=%t --kind %s", dryRun, kind)
		}
		require.ErrorContains(t, publish("lanes"), "unsupported serving object kind")
		require.ErrorContains(t, publish(string(policyregistry.ServingProposal)), "requires a v2 schema; v1 objects are read-only")
	}
	require.Zero(t, opened, "kind and schema rejections are offered before the registry is opened")
	malformed := cliManifestFile(t, []byte(`{"schema_version":"router_serving_proposal_v2","unknown":true}`))
	require.Error(t, runServingWith(ctx, []string{string(commandPublish), "--dry-run", "--kind", string(policyregistry.ServingProposal), "--manifest", malformed}, dependencies))
	require.Nil(t, output)
}

func TestWorkflowActorIgnoresCallerOverride(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		env      map[string]string
		expected string
	}{
		{
			name:     "GitHub run",
			env:      map[string]string{"GITHUB_ACTOR": "ci-bot", "GITHUB_RUN_ID": "4242", "USER": "local-operator"},
			expected: "ci-bot@run:4242",
		},
		{
			name:     "local operator",
			env:      map[string]string{"USER": "local-operator"},
			expected: "local-operator",
		},
		{
			name:     "proposal actor",
			env:      map[string]string{},
			expected: "proposal-operator",
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			scenario.env["WORKFLOW_ACTOR"] = "spoofed-operator"
			actor := workflowActorFor(func(key string) string { return scenario.env[key] }, policyregistry.ProposalView{Actor: "proposal-operator"})
			require.Equal(t, scenario.expected, actor)
		})
	}
}

func TestServingCLIApplyResolvesDigestDryRunsActivatesAndReplays(t *testing.T) {
	registry, endpoints, fixture := cliServingFixture(t)
	proposal := cliV2Proposal(fixture, "run-1:lane-0")
	ref := cliPublish(t, registry, policyregistry.ServingProposal, proposal)
	require.Contains(t, ref.URI, "/artifacts/")
	path := cliProposalFile(t, ref)
	var output any
	env := map[string]string{"WORKFLOW_ACTOR": "github-actions:example/workflows:4242:1", "GITHUB_ACTOR": "ci-bot", "GITHUB_RUN_ID": "4242", "USER": "local-operator"}
	dependencies := cliDependencies(registry, endpoints, &output, env)
	ctx := context.Background()

	require.NoError(t, runServingWith(ctx, []string{string(commandApply), "--dry-run", "--proposal-sha256", ref.SHA256}, dependencies))
	prepared := output.(policyregistry.PreparationResult)
	require.True(t, prepared.Prepared)
	require.Equal(t, ref, prepared.Proposal, "--proposal-sha256 resolves to the exact artifacts/ reference")
	require.Nil(t, prepared.Activation)
	require.Zero(t, registry.writes, "a dry run never writes state")
	require.Zero(t, registry.state.Generation)

	require.NoError(t, runServingWith(ctx, []string{string(commandApply), "--proposal", path}, dependencies))
	first := output.(policyregistry.ActivationResult)
	require.False(t, first.Replayed)
	require.Equal(t, policyregistry.ActivationCurrent, first.Outcome)
	require.Equal(t, ref, first.Activation.Proposal)
	require.Equal(t, "v2-operator", first.Activation.Actor)
	require.Equal(t, "ci-bot@run:4242", first.Activation.WorkflowActor, "the GitHub run identity ignores the caller-supplied override")
	require.EqualValues(t, 1, first.Snapshot.Generation)
	require.Equal(t, 1, registry.writes)

	endpoints.err = errors.New("destination offline")
	require.NoError(t, runServingWith(ctx, []string{string(commandApply), "--proposal-sha256", ref.SHA256}, dependencies))
	replayed := output.(policyregistry.ActivationResult)
	require.True(t, replayed.Replayed, "the same proposal replays its original outcome without revalidating destinations")
	require.Equal(t, first.Activation.ID, replayed.Activation.ID)
	require.Equal(t, 1, registry.writes)
	require.NoError(t, runServingWith(ctx, []string{string(commandApply), "--dry-run", "--proposal", path}, dependencies))
	reconciled := output.(policyregistry.PreparationResult)
	require.False(t, reconciled.Prepared)
	require.Equal(t, first.Activation.ID, reconciled.Activation.Activation.ID)

	require.NoError(t, runServingWith(ctx, []string{string(commandStatus), "--proposal", path}, dependencies))
	status := output.(policyregistry.ActivationResult)
	require.True(t, status.Replayed)
	require.Equal(t, first.Activation.ID, status.Activation.ID)
	require.NoError(t, runServingWith(ctx, []string{string(commandStatus), "--target", string(fixture.Target)}, dependencies))
	require.Equal(t, first.Activation.ID, output.(policyregistry.ServingStateSnapshot).State.CurrentActivationID)

	pending := cliProposalFile(t, cliPublish(t, registry, policyregistry.ServingProposal, cliV2Proposal(fixture, "run-2:lane-0")))
	require.NoError(t, runServingWith(ctx, []string{string(commandStatus), "--proposal", pending}, dependencies))
	encoded, err := json.Marshal(output)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"activated":false`, "a published but unapplied proposal reports no activation")
	unpublished := cliProposalFile(t, policyregistry.ObjectRef{URI: defaultRegistryURI + "/artifacts/" + strings.Repeat("e", 64) + ".json", SHA256: strings.Repeat("e", 64), Generation: 1})
	require.ErrorIs(t, runServingWith(ctx, []string{string(commandStatus), "--proposal", unpublished}, dependencies), policyregistry.ErrNotFound)
	require.ErrorIs(t, runServingWith(ctx, []string{string(commandApply), "--proposal-sha256", strings.Repeat("e", 64)}, dependencies), policyregistry.ErrNotFound)
	require.Equal(t, 1, registry.writes)
}

func TestServingCLIRollbackRequiresRollbackScope(t *testing.T) {
	registry, endpoints, fixture := cliServingFixture(t)
	var output any
	dependencies := cliDependencies(registry, endpoints, &output, map[string]string{"USER": "local-operator"})
	ctx := context.Background()
	require.NoError(t, runServingWith(ctx, []string{string(commandApply), "--proposal", cliProposalFile(t, cliPublish(t, registry, policyregistry.ServingProposal, cliV2Proposal(fixture, "run-1:lane-0")))}, dependencies))
	first := output.(policyregistry.ActivationResult)

	forward := cliV2Proposal(fixture, "run-2:lane-0")
	forward.PreviousSelectionSet = &fixture.SelectionSet
	forward.WithdrawActivations = []string{first.Activation.ID}
	forwardPath := cliProposalFile(t, cliPublish(t, registry, policyregistry.ServingProposal, forward))
	err := runServingWith(ctx, []string{string(commandRollback), "--proposal", forwardPath}, dependencies)
	require.ErrorContains(t, err, `requires a proposal with scope "rollback", got "full"`)
	require.Equal(t, 1, registry.writes, "a rejected rollback never reaches the CAS write")
	require.Equal(t, first.Activation.ID, registry.state.State.CurrentActivationID)

	rollback := forward
	rollback.Scope = policyregistry.ChangeRollback
	rollback.RequestID = "run-3:lane-0"
	rollbackRef := cliPublish(t, registry, policyregistry.ServingProposal, rollback)
	rollbackPath := cliProposalFile(t, rollbackRef)
	require.NoError(t, runServingWith(ctx, []string{string(commandRollback), "--proposal", rollbackPath}, dependencies))
	rolled := output.(policyregistry.ActivationResult)
	require.NotEqual(t, first.Activation.ID, rolled.Activation.ID)
	require.Equal(t, "local-operator", rolled.Activation.WorkflowActor, "$USER is the fallback when no GitHub run is present")
	require.Equal(t, rolled.Activation.ID, rolled.Snapshot.State.Activations[first.Activation.ID].ReplacementID)
	require.EqualValues(t, 2, rolled.Snapshot.Generation)
	require.Equal(t, 2, registry.writes)

	require.NoError(t, runServingWith(ctx, []string{string(commandRollback), "--proposal", rollbackPath}, dependencies))
	require.True(t, output.(policyregistry.ActivationResult).Replayed)
	require.NoError(t, runServingWith(ctx, []string{string(commandStatus), "--proposal", rollbackPath}, dependencies))
	require.Equal(t, rolled.Activation.ID, output.(policyregistry.ActivationResult).Activation.ID, "status reconciles by proposal ref, not request ID")
	require.Equal(t, 2, registry.writes)

	unknownSource := rollback
	unknownSource.RequestID = "run-4:lane-0"
	unknownSource.SourceCandidate = policyregistry.ObjectRef{URI: defaultRegistryURI + "/artifacts/" + strings.Repeat("a", 64) + ".json", SHA256: strings.Repeat("a", 64), Generation: 1}
	require.ErrorContains(t, runServingWith(ctx, []string{string(commandRollback), "--proposal", cliProposalFile(t, cliPublish(t, registry, policyregistry.ServingProposal, unknownSource))}, dependencies), "rollback requires a known-good source release")
	require.Equal(t, 2, registry.writes)
}

func TestServingCLIApplyAcceptsV1ProposalsAndFreezesStaleGenerations(t *testing.T) {
	registry, endpoints, proposal := cliServingFixture(t)
	var output any
	dependencies := cliDependencies(registry, endpoints, &output, map[string]string{"GITHUB_ACTOR": "ci-bot", "GITHUB_RUN_ID": "4243"})
	ctx := context.Background()
	ref := cliPublish(t, registry, policyregistry.ServingProposals, proposal)
	require.Contains(t, ref.URI, "/router_serving/v1/proposals/")
	require.NoError(t, runServingWith(ctx, []string{string(commandApply), "--proposal-sha256", ref.SHA256}, dependencies))
	activated := output.(policyregistry.ActivationResult)
	require.Equal(t, ref, activated.Activation.Proposal, "legacy proposal digests resolve through the v1 namespace")
	require.Equal(t, "original-operator", activated.Activation.Actor)
	require.EqualValues(t, 1, activated.Snapshot.Generation)

	proposal.RequestID = "run-2:lane-0"
	stale := cliProposalFile(t, cliPublish(t, registry, policyregistry.ServingProposals, proposal))
	require.ErrorIs(t, runServingWith(ctx, []string{string(commandApply), "--dry-run", "--proposal", stale}, dependencies), policyregistry.ErrConflict, "v1 proposals still carry expected_generation and freeze against it")
	require.ErrorIs(t, runServingWith(ctx, []string{string(commandApply), "--proposal", stale}, dependencies), policyregistry.ErrConflict)
	require.Equal(t, 1, registry.writes)
}

func TestServingCLIApplyCommittedOutputFailureReconcilesThroughStatus(t *testing.T) {
	registry, endpoints, fixture := cliServingFixture(t)
	path := cliProposalFile(t, cliPublish(t, registry, policyregistry.ServingProposal, cliV2Proposal(fixture, "run-1:lane-0")))
	failOutput := true
	var output any
	dependencies := cliDependencies(registry, endpoints, &output, nil)
	dependencies.getenv = nil
	dependencies.writeOutput = func(value any) error {
		if failOutput {
			return errors.New("broken output pipe")
		}
		output = value
		return nil
	}
	ctx := context.Background()
	require.ErrorContains(t, runServingWith(ctx, []string{string(commandApply), "--proposal", path}, dependencies), "activated; output observation degraded")
	require.Equal(t, 1, registry.writes)
	failOutput = false
	require.NoError(t, runServingWith(ctx, []string{string(commandStatus), "--proposal", path}, dependencies))
	reconciled := output.(policyregistry.ActivationResult)
	require.True(t, reconciled.Replayed)
	require.Equal(t, "v2-operator", reconciled.Activation.WorkflowActor, "without a GitHub run or $USER the proposal operator is the executing identity")
	require.Equal(t, policyregistry.ActivationCurrent, reconciled.Outcome)
	require.Equal(t, 1, registry.writes)
}

func TestServingCLIRejectsRetiredApprovalFlagsBeforeReadingTheProposal(t *testing.T) {
	registry, endpoints, fixture := cliServingFixture(t)
	path := cliProposalFile(t, cliPublish(t, registry, policyregistry.ServingProposal, cliV2Proposal(fixture, "run-1:lane-0")))
	var output any
	dependencies := cliDependencies(registry, endpoints, &output, map[string]string{"GITHUB_ACTOR": "ci-bot", "GITHUB_RUN_ID": "4242"})
	for _, command := range []commandName{commandApply, commandRollback} {
		for flagName, value := range map[string]string{"approved-proposal": strings.Repeat("f", 64), "workflow-actor": "workflow-service", "validation-origin": "https://ignored.example", "stored": ""} {
			args := []string{string(command), "--proposal", path, "--" + flagName}
			if value != "" {
				args = append(args, value)
			}
			require.ErrorContains(t, runServingWith(context.Background(), args, dependencies), "flag provided but not defined: -"+flagName, "%s --%s", command, flagName)
		}
	}
	require.Nil(t, output)
	require.Zero(t, registry.writes)
}

func TestServingCLIRemovedVerbsPointToTheirReplacement(t *testing.T) {
	opened := 0
	dependencies := servingDependencies{openRegistry: func(context.Context, string) (servingRegistry, error) {
		opened++
		return nil, errors.New("registry must not be opened for a removed verb")
	}}
	for verb, replacement := range map[string]string{"validate": "publish --dry-run", "resolve": "apply --proposal-sha256", "prepare": "apply --dry-run", "activate": "`policyctl serving apply`"} {
		err := runServingWith(context.Background(), []string{verb, "--proposal-sha256", strings.Repeat("a", 64)}, dependencies)
		require.ErrorContains(t, err, "serving "+verb+" was removed", verb)
		require.ErrorContains(t, err, replacement, verb)
	}
	require.Zero(t, opened)
	require.ErrorContains(t, runServingWith(context.Background(), []string{"promote"}, dependencies), `unsupported serving command "promote"`)
	require.ErrorContains(t, runServingWith(context.Background(), nil, dependencies), "usage: policyctl serving <publish|apply|status|rollback>")
}
