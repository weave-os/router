package policyregistry_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
)

type stubRouter struct{ router.Router }

func TestServingRuntimeCacheReusesExactAdmission(t *testing.T) {
	store, _, set := controllerFixture(t)
	builds := 0
	cache, err := policyregistry.NewServingRuntimeCache(store, func(context.Context, policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		builds++
		return map[router.Strategy]router.Router{router.StrategyHMM: stubRouter{}}, nil
	})
	require.NoError(t, err)
	admission := policyregistry.SessionReleaseBinding{Target: policyregistry.TargetStable, ActivationID: "activation", Selection: set.Default, BindingGeneration: 4}
	first, err := cache.Snapshot(context.Background(), admission)
	require.NoError(t, err)
	second, err := cache.Snapshot(context.Background(), admission)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, builds)
	require.Equal(t, int64(4), first.HeadSnapshot.Generation)
	ctx := policyregistry.WithServingSnapshot(context.Background(), first)
	require.Equal(t, first, policyregistry.ServingSnapshotFromContext(ctx))
}
