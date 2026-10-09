package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/server"
)

type routeWarmupFake struct {
	failures int
	calls    int
}

func (fake *routeWarmupFake) Route(_ context.Context, request router.Request) (router.Decision, error) {
	fake.calls++
	if request.PromptText != "Synthetic local HMM router readiness warm-up." {
		return router.Decision{}, errors.New("unexpected warm-up request")
	}
	if fake.calls <= fake.failures {
		return router.Decision{}, errors.New("classifier is warming")
	}
	return router.Decision{}, nil
}

func TestLocalServingIdentityRequiresExactLoopbackSnapshot(t *testing.T) {
	t.Setenv("ROUTER_LOCAL_POLICY_TARGET", "prod/stable")
	t.Setenv("ROUTER_LOCAL_SELECTION_SET_SHA256", strings.Repeat("a", 64))
	t.Setenv("ROUTER_LOCAL_SELECTION_SET_URI", "gs://synthetic/artifacts/selection.json")
	t.Setenv("ROUTER_LOCAL_SELECTION_SET_GENERATION", "2")
	t.Setenv("ROUTER_LOCAL_ROUTER_REVISION", strings.Repeat("b", 40))
	t.Setenv("ROUTER_LOCAL_CLASSIFIER_SHA256", strings.Repeat("c", 64))
	t.Setenv("ROUTER_LOCAL_CLASSIFIER_URL", "http://127.0.0.1:8093")
	identity, err := localServingIdentityFromEnv(server.DeploymentModeSelfHosted)
	require.NoError(t, err)
	require.Equal(t, "prod/stable", string(identity.Target))

	_, err = localServingIdentityFromEnv(server.DeploymentModeManaged)
	require.ErrorContains(t, err, "selfhosted")
	t.Setenv("ROUTER_LOCAL_CLASSIFIER_URL", "http://classifier.example:8093")
	_, err = localServingIdentityFromEnv(server.DeploymentModeSelfHosted)
	require.ErrorContains(t, err, "loopback")
	t.Setenv("ROUTER_LOCAL_CLASSIFIER_URL", "http://127.0.0.1:8093")
	t.Setenv("ROUTER_LOCAL_SELECTION_SET_SHA256", strings.Repeat("A", 64))
	_, err = localServingIdentityFromEnv(server.DeploymentModeSelfHosted)
	require.ErrorContains(t, err, "lowercase SHA-256")
}

func TestWarmLocalHMMRouteRetriesOnceBeforeReadiness(t *testing.T) {
	fake := &routeWarmupFake{failures: 1}

	err := warmLocalHMMRoute(context.Background(), fake)

	require.NoError(t, err)
	require.Equal(t, 2, fake.calls)
}

func TestWarmLocalHMMRouteFailsStartupAfterBoundedAttempts(t *testing.T) {
	fake := &routeWarmupFake{failures: localServingRouteWarmupAttempts}

	err := warmLocalHMMRoute(context.Background(), fake)

	require.ErrorContains(t, err, "failed after 2 attempts")
	require.Equal(t, localServingRouteWarmupAttempts, fake.calls)
}

func TestWarmLocalHMMRouteHonorsStartupDeadline(t *testing.T) {
	fake := &routeWarmupFake{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := warmLocalHMMRoute(ctx, fake)

	require.ErrorContains(t, err, "stopped before attempt 1")
	require.Zero(t, fake.calls)
}
