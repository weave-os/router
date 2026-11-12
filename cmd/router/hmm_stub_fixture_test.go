package main

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/smoke/hmmstub"
)

// The smoke and legacy-runtime fixtures boot the worker on the stub release; it
// must stay loadable by the same builder production uses for hmm_embedding.
func TestHMMStubReleaseServesTheEmbeddingDefault(t *testing.T) {
	sidecar := httptest.NewServer(hmmstub.Handler())
	t.Cleanup(sidecar.Close)
	dir := t.TempDir()
	require.NoError(t, hmmstub.WriteRelease(dir))

	registry, err := policyregistry.NewDirectoryRegistry(dir, sidecar.URL)
	require.NoError(t, err)
	manager, err := policyregistry.NewManager(registry, registry.Environment(), registry.Lane(),
		hmmPolicySnapshotBuilder(map[string]struct{}{providers.ProviderOpenAI: {}}, []router.Strategy{router.StrategyHMMEmbedding}, policySidecarAuthNone, time.Second, time.Second),
		slog.Default())
	require.NoError(t, err)
	require.NoError(t, manager.Refresh(context.Background()))

	decision, err := policyregistry.NewDynamicRouter(manager, router.StrategyHMMEmbedding).
		Route(context.Background(), router.Request{PromptText: "Explain a binary search."})
	require.NoError(t, err)
	assert.Equal(t, providers.ProviderOpenAI, decision.Provider)
	assert.NotEmpty(t, decision.Model)
}
