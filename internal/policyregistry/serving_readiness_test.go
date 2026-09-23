package policyregistry_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
)

func TestPrepareWorkerDoesNotDependOnBootstrapClassifierOrOtherTargets(t *testing.T) {
	store, _, set := controllerFixture(t)
	binding := *store.object(t, policyregistry.ServingBindings, set.Default.Binding).(*policyregistry.DeploymentBinding)
	identity := policyregistry.WorkerIdentity{Target: binding.Target, Project: binding.Project, Region: binding.Region, Revision: binding.Router.Name, ImageDigest: binding.Router.ImageDigest, Configuration: binding.Router.Configuration}
	builds := 0
	cache, err := policyregistry.NewServingRuntimeCache(store, func(context.Context, policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		builds++
		return nil, errors.New("bootstrap classifier retired")
	})
	require.NoError(t, err)
	baseline, err := cache.PrepareWorker(context.Background(), identity, servingRef(t, policyregistry.ServingSelectionSets, set))
	require.NoError(t, err, "immutable closure readiness must not depend on a retired classifier or absent lane heads")
	require.NotNil(t, baseline.Policy)
	require.Empty(t, baseline.Routers, "bootstrap closure is never an inference runtime")
	require.Zero(t, builds)
	runtime := policyregistry.NewAdmittedRouter(router.StrategyHMM, baseline)
	require.True(t, runtime.Available())
	_, err = runtime.Route(context.Background(), router.Request{})
	require.ErrorIs(t, err, policyregistry.ErrNoActivePolicy)
	_, err = cache.Snapshot(context.Background(), policyregistry.SessionReleaseBinding{Target: set.Target, Selection: set.Default})
	require.ErrorContains(t, err, "bootstrap classifier retired", "actual admission still requires its own live classifier")
	require.Equal(t, 1, builds)

	for name, mutate := range map[string]func(*policyregistry.WorkerIdentity){
		"target":        func(w *policyregistry.WorkerIdentity) { w.Target = policyregistry.TargetInternal },
		"image":         func(w *policyregistry.WorkerIdentity) { w.ImageDigest = "sha256:" + strings.Repeat("9", 64) },
		"configuration": func(w *policyregistry.WorkerIdentity) { w.Configuration = artifactRef("different-config") },
		"revision":      func(w *policyregistry.WorkerIdentity) { w.Revision = "another-revision" },
	} {
		t.Run(name, func(t *testing.T) {
			wrong := identity
			mutate(&wrong)
			_, err := cache.PrepareWorker(context.Background(), wrong, servingRef(t, policyregistry.ServingSelectionSets, set))
			require.Error(t, err)
		})
	}
}

func TestPrepareWorkerRejectsUnavailableAssignedProfile(t *testing.T) {
	store, _, set := controllerFixture(t)
	binding := *store.object(t, policyregistry.ServingBindings, set.Default.Binding).(*policyregistry.DeploymentBinding)
	identity := policyregistry.WorkerIdentity{Target: binding.Target, Project: binding.Project, Region: binding.Region, Revision: binding.Router.Name, ImageDigest: binding.Router.ImageDigest, Configuration: binding.Router.Configuration}
	profile := namespaceRef(policyregistry.ServingProfiles, "unavailable")
	selection := set.Default
	selection.Profile = &profile
	set.Profiles[profileKeyOne] = selection
	ref := store.publish(t, policyregistry.ServingSelectionSets, set)
	cache, err := policyregistry.NewServingRuntimeCache(store, func(context.Context, policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		t.Fatal("bootstrap must not contact classifiers")
		return nil, nil
	})
	require.NoError(t, err)
	_, err = cache.PrepareWorker(context.Background(), identity, ref)
	require.ErrorIs(t, err, policyregistry.ErrNotFound, "default availability cannot hide an invalid customer profile")
}
