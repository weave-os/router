package main

import (
	"github.com/stretchr/testify/require"
	"testing"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server"
)

func TestManagedServingUsesFleetTarget(t *testing.T) {
	t.Setenv("ROUTER_SERVING_TARGET", "")
	require.False(t, managedServingEnabled(server.DeploymentModeManaged))
	t.Setenv("ROUTER_SERVING_TARGET", "prod/stable")
	require.True(t, managedServingEnabled(server.DeploymentModeManaged))
	require.False(t, managedServingEnabled(server.DeploymentModeSelfHosted))
}
func TestValidateManagedServingBoot(t *testing.T) {
	for _, tc := range []struct {
		name, target, strategy string
		valid                  bool
	}{
		{"stable", "prod/stable", "hmm_embedding", true},
		{"internal", "prod/weave-internal", "hmm", true},
		{"staging", "staging", "hmm_embedding", true},
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
	require.NotContains(t, managedServingStrategies, router.StrategyCluster)
	require.NotContains(t, managedServingStrategies, router.StrategyHMMBeta)
}
