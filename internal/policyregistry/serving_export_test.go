package policyregistry_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/rosterdata"
)

func (s *servingMemoryStore) ReadPolicyPayload(ctx context.Context, ref policyregistry.ObjectRef) ([]byte, error) {
	policy, err := s.ReadServingPolicy(ctx, ref)
	if err != nil {
		return nil, err
	}
	return rosterdata.CanonicalBytes(policy)
}

func activateCohort(t *testing.T) (*servingMemoryStore, policyregistry.SelectionSetV3) {
	t.Helper()
	store, set := cohortFixture(t)
	proposal := cohortProposal(t, store, set, nil, policyregistry.ChangeFull, "")
	transition, err := policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, servingPayload(t, proposal), proposalRef(t, proposal), testRegistryRoot, "workflow", servingEpoch)
	require.NoError(t, err)
	_, err = store.CompareAndSwapServingState(context.Background(), transition.Snapshot.State, 0)
	require.NoError(t, err)
	return store, set
}

func TestExportServingTargetServesActiveDefaultLaneFromDirectory(t *testing.T) {
	store, set := activateCohort(t)
	candidate := store.object(t, policyregistry.ServingCandidate, set.Code.Candidate).(*policyregistry.CandidateV2)
	dir := t.TempDir()
	exported, err := policyregistry.ExportServingTarget(context.Background(), store, set.Target, dir, "")
	require.NoError(t, err)
	require.Equal(t, set.Code.Candidate, exported.Candidate)
	require.Equal(t, candidate.Classifier.Package, exported.Package)

	local, err := policyregistry.NewDirectoryRegistry(dir, "http://hmm-sidecar:8093")
	require.NoError(t, err)
	head, err := local.ReadHead(context.Background(), local.Environment(), policyregistry.LaneStable)
	require.NoError(t, err)
	require.Equal(t, "http://hmm-sidecar:8093", head.Head.ClassifierRevisionURL)
	release, err := local.ReadRelease(context.Background(), policyregistry.ObjectRef{URI: head.Head.ReleaseURI, SHA256: head.Head.ReleaseSHA256, Generation: head.Head.ReleaseGeneration})
	require.NoError(t, err)
	require.Equal(t, candidate.Classifier.Identity, release.Classifier)
	require.Equal(t, set.DefaultPolicy, release.Policy)
	_, err = local.ReadPolicy(context.Background(), policyregistry.ObjectRef{URI: release.Policy.URI, SHA256: release.Policy.SHA256, Generation: release.Policy.Generation})
	require.NoError(t, err)
}

func TestExportServingTargetRepinsClassifierImage(t *testing.T) {
	store, set := activateCohort(t)
	dir := t.TempDir()
	digest := "sha256:" + policyregistry.Digest([]byte("self-hosted-classifier"))
	_, err := policyregistry.ExportServingTarget(context.Background(), store, set.Target, dir, digest)
	require.NoError(t, err)
	local, err := policyregistry.NewDirectoryRegistry(dir, "")
	require.NoError(t, err)
	head, err := local.ReadHead(context.Background(), local.Environment(), policyregistry.LaneStable)
	require.NoError(t, err)
	release, err := local.ReadRelease(context.Background(), policyregistry.ObjectRef{URI: head.Head.ReleaseURI, SHA256: head.Head.ReleaseSHA256, Generation: head.Head.ReleaseGeneration})
	require.NoError(t, err)
	require.Equal(t, digest, release.Classifier.ImageDigest)
}

func TestExportServingTargetRequiresAnActivation(t *testing.T) {
	store, set := cohortFixture(t)
	_, err := policyregistry.ExportServingTarget(context.Background(), store, set.Target, t.TempDir(), "")
	require.Error(t, err)
}
