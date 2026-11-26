package main

import (
	"bytes"
	"context"
	"log/slog"
	"weave-os/router/smoke/hmmstub"

	"github.com/stretchr/testify/require"
	"testing"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server"
)

func TestServingRuntimeFollowsFleetTarget(t *testing.T) {
	t.Setenv("ROUTER_SERVING_TARGET", "")
	require.False(t, servingTargetConfigured())
	t.Setenv("ROUTER_SERVING_TARGET", "prod/stable")
	require.True(t, servingTargetConfigured())
}
func TestValidateManagedServingBoot(t *testing.T) {
	for _, tc := range []struct {
		name, target, strategy string
		valid                  bool
	}{
		{"stable", "prod/stable", "hmm_embedding", true},
		{"internal", "prod/weave-internal", "hmm", true},
		{"staging", "staging", "hmm_embedding", true},
		{"beta", "prod/beta", "hmm_embedding", true},
		{"missing target", "", "hmm_embedding", false},
		{"unknown fleet", "beta", "hmm_embedding", false},
		{"missing strategy", "prod/stable", "", false},
		{"cluster", "prod/stable", "cluster", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"ROUTER_SERVING_TARGET": tc.target, envDefaultStrategy: tc.strategy}
			err := validateManagedServingBoot(server.DeploymentModeManaged, func(key string) (string, bool) { v, ok := env[key]; return v, ok })
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	require.NoError(t, validateManagedServingBoot(server.DeploymentModeManaged, func(string) (string, bool) { return "", false }))
}
func TestValidateManagedServingBootRejectsEveryStampAlone(t *testing.T) {
	for _, name := range managedServingEnvVars {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateManagedServingBoot(server.DeploymentModeManaged, func(key string) (string, bool) { return "stamped", key == name }))
		})
	}
}
func TestManagedServingStrategiesMatchRuntimeRegistration(t *testing.T) {
	for _, strategy := range managedServingStrategies {
		require.True(t, router.IsHMMStrategy(strategy))
	}
	require.NotContains(t, managedServingStrategies, router.Strategy("cluster"))
	require.NotContains(t, managedServingStrategies, router.StrategyHMMBeta)
}

func TestManagedWorkersRefuseLocalServingRegistry(t *testing.T) {
	t.Setenv("ROUTER_ALLOW_LOCAL_SERVING_REGISTRY", "")
	_, _, err := openServingStore(context.Background(), server.DeploymentModeManaged, t.TempDir())
	require.ErrorContains(t, err, "managed workers require a gs:// serving registry")
}

func TestManagedWorkersOpenLocalServingRegistryWhenAllowed(t *testing.T) {
	t.Setenv("ROUTER_ALLOW_LOCAL_SERVING_REGISTRY", "true")
	graph, err := hmmstub.NewServingGraph("http://hmm-stub:8093")
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, graph.Write(dir))
	store, closeStore, err := openServingStore(context.Background(), server.DeploymentModeManaged, dir)
	require.NoError(t, err)
	t.Cleanup(closeStore)
	require.Equal(t, "gs://hmm-stub-fixture/registry", store.RootURI())
}

func TestManagedBootRefusesLocalDevPolicyRelease(t *testing.T) {
	for _, name := range []string{"ROUTER_POLICY_ENVIRONMENT", "ROUTER_POLICY_RELEASE_DIR"} {
		t.Run(name, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				if key == name {
					return "prod", true
				}
				return "", false
			}
			require.ErrorContains(t, validateManagedServingBoot(server.DeploymentModeManaged, lookup), name+" is a local-dev-only policy release reader")
			require.NoError(t, validateManagedServingBoot(server.DeploymentModeSelfHosted, lookup))
		})
	}
}

func TestLocalServingRegistryOptInLogsAStartupWarning(t *testing.T) {
	t.Setenv("ROUTER_ALLOW_LOCAL_SERVING_REGISTRY", "true")
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	graph, err := hmmstub.NewServingGraph("http://hmm-stub:8093")
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, graph.Write(dir))
	_, closeStore, err := openServingStore(context.Background(), server.DeploymentModeSelfHosted, dir)
	require.NoError(t, err)
	t.Cleanup(closeStore)
	require.Contains(t, logs.String(), "ROUTER_ALLOW_LOCAL_SERVING_REGISTRY must only be set for a local stack")
	require.Contains(t, logs.String(), "level=WARN")
}

func TestManagedWorkerRefusesToStartWithoutItsChannel(t *testing.T) {
	require.ErrorIs(t, policyregistry.ValidateWorkerChannel(policyregistry.TargetBeta, ""), policyregistry.ErrServingWorkerUnscoped,
		"a managed beta worker must refuse to start without its channel")
	require.ErrorIs(t, policyregistry.ValidateWorkerChannel("", ""), policyregistry.ErrServingWorkerUnscoped,
		"a managed worker must refuse to start without an identity")
	for _, target := range []policyregistry.ServingTarget{policyregistry.TargetStable, policyregistry.TargetInternal, policyregistry.TargetBeta, policyregistry.TargetStaging} {
		channel, _ := target.Channel()
		require.NoError(t, policyregistry.ValidateWorkerChannel(target, channel), target)
	}
}
