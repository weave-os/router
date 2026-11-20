package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/smoke/hmmstub"
)

// The local stack boots the managed worker on the stub's v5 serving registry;
// it must stay preparable by the same runtime production uses.
func TestHMMStubServingRegistryPreparesTheManagedWorker(t *testing.T) {
	classifier := httptest.NewServer(hmmstub.Handler())
	t.Cleanup(classifier.Close)
	graph, err := hmmstub.NewServingGraph(classifier.URL)
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, graph.Write(dir))

	store, closeStore, err := openServingStore(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(closeStore)
	cache, err := policyregistry.NewServingRuntimeCache(store, hmmPolicySnapshotBuilder(
		map[string]struct{}{providers.ProviderOpenAI: {}}, managedServingStrategies, policySidecarAuthNone, time.Second, time.Second,
	))
	require.NoError(t, err)
	identity := policyregistry.WorkerIdentity{
		Target: hmmstub.ServingTarget, Project: hmmstub.ServingProject, Region: hmmstub.ServingRegion,
		Revision: hmmstub.ServingRevision, ImageDigest: hmmstub.ServingImageDigest, Configuration: hmmstub.ServingConfiguration(),
	}
	baseline, err := cache.PrepareWorker(context.Background(), identity, graph.SelectionSet)
	require.NoError(t, err)
	require.NotNil(t, baseline.Policy)
	require.Contains(t, managedServingStrategies, router.StrategyHMMEmbedding)
}
