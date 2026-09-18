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
	require.Zero(t, first.HeadSnapshot.Generation, "cached bytes must not inherit the first session's generation")
	ctx := policyregistry.WithServingSnapshot(context.Background(), first)
	require.Equal(t, first, policyregistry.ServingSnapshotFromContext(ctx))
	admission.Selection.Profile = &policyregistry.ObjectRef{}
	_, err = cache.Snapshot(context.Background(), admission)
	require.Error(t, err, "an invalid non-nil profile cannot reuse a cached default snapshot")
}

func TestServingRuntimeCacheCannotBypassProfileOwnershipOrExactReferences(t *testing.T) {
	store, _, set := controllerFixture(t)
	base := store.objects[set.Default.Release].(*policyregistry.ServingRelease)
	profile := registerProfileFixture(t, store, set.Default, profileKeyOne, base.Policy)
	cache, err := policyregistry.NewServingRuntimeCache(store, func(context.Context, policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		return map[router.Strategy]router.Router{router.StrategyHMM: stubRouter{}}, nil
	})
	require.NoError(t, err)
	admission := policyregistry.SessionReleaseBinding{Target: set.Target, ProfileKey: profileKeyOne, Selection: profile}
	_, err = cache.Snapshot(context.Background(), admission)
	require.NoError(t, err)
	for _, mutate := range []func(*policyregistry.SessionReleaseBinding){
		func(a *policyregistry.SessionReleaseBinding) { a.ProfileKey = profileKeyTwo },
		func(a *policyregistry.SessionReleaseBinding) { a.Target = policyregistry.TargetInternal },
		func(a *policyregistry.SessionReleaseBinding) { a.Selection.Release.Generation++ },
		func(a *policyregistry.SessionReleaseBinding) { a.Selection.Binding.Generation++ },
		func(a *policyregistry.SessionReleaseBinding) {
			ref := *a.Selection.Profile
			ref.Generation++
			a.Selection.Profile = &ref
		},
		func(a *policyregistry.SessionReleaseBinding) {
			a.Selection.Release.URI = "gs://unapproved/" + a.Selection.Release.SHA256
		},
	} {
		invalid := admission
		mutate(&invalid)
		_, err := cache.Snapshot(context.Background(), invalid)
		require.Error(t, err)
	}
}
