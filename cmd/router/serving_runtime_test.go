package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/server"
)

func TestManagedServingRequiresExplicitAssertionKey(t *testing.T) {
	// Future deployment values must not opt an existing worker into new dependencies.
	t.Setenv("ROUTER_SERVING_REGISTRY_URI", "invalid-disabled-registry")
	t.Setenv("ROUTER_SERVING_TARGET", "invalid-disabled-target")
	t.Setenv("ROUTER_SERVING_CONFIGURATION_GENERATION", "invalid-disabled-generation")
	for _, key := range []string{"", " \t\n"} {
		t.Setenv("ROUTER_SERVING_ASSERTION_KEY", key)
		assert.False(t, managedServingEnabled())
	}
	t.Setenv("ROUTER_SERVING_ASSERTION_KEY", strings.Repeat("k", 32))
	assert.True(t, managedServingEnabled())
}

func TestValidateManagedServingBoot(t *testing.T) {
	fullServingEnv := map[string]string{
		"ROUTER_SERVING_TARGET":                   "prod-weave-internal",
		"ROUTER_SERVING_PROJECT":                  "weave-prod",
		"ROUTER_SERVING_REGION":                   "us-central1",
		"ROUTER_SERVING_IMAGE_DIGEST":             "sha256:" + strings.Repeat("a", 64),
		"ROUTER_SERVING_REGISTRY_URI":             "gs://weave_ml/weave_registry",
		"ROUTER_SERVING_CONFIGURATION_URI":        "gs://weave_ml/weave_registry/artifacts/config.json",
		"ROUTER_SERVING_CONFIGURATION_SHA256":     strings.Repeat("b", 64),
		"ROUTER_SERVING_CONFIGURATION_GENERATION": "17",
		"ROUTER_SERVING_SELECTION_SET_URI":        "gs://weave_ml/weave_registry/artifacts/selection.json",
		"ROUTER_SERVING_SELECTION_SET_SHA256":     strings.Repeat("c", 64),
		"ROUTER_SERVING_SELECTION_SET_GENERATION": "23",
	}
	servingEnvWithKey := map[string]string{envServingAssertionKey: strings.Repeat("k", 32), envDefaultStrategy: "hmm_embedding"}
	for name, value := range fullServingEnv {
		servingEnvWithKey[name] = value
	}
	withDefaultStrategy := func(value string) map[string]string {
		env := map[string]string{}
		for name, v := range servingEnvWithKey {
			env[name] = v
		}
		env[envDefaultStrategy] = value
		return env
	}
	withoutDefaultStrategy := map[string]string{}
	for name, value := range servingEnvWithKey {
		if name != envDefaultStrategy {
			withoutDefaultStrategy[name] = value
		}
	}

	tests := []struct {
		name         string
		mode         server.DeploymentMode
		env          map[string]string
		wantContains []string
	}{
		{name: "managed serving worker with key", mode: server.DeploymentModeManaged, env: servingEnvWithKey},
		{name: "managed serving worker with hmm default", mode: server.DeploymentModeManaged, env: withDefaultStrategy("hmm")},
		{name: "managed serving worker with unnormalized default", mode: server.DeploymentModeManaged, env: withDefaultStrategy(" HMM_Embedding\n")},
		// A revision minted without ROUTER_DEFAULT_STRATEGY would score every
		// hmm_beta/NULL installation on the legacy cluster embedder.
		{
			name: "managed serving worker without default strategy", mode: server.DeploymentModeManaged, env: withoutDefaultStrategy,
			wantContains: []string{envDefaultStrategy, "hmm, hmm_embedding"},
		},
		{
			name: "managed serving worker with blank default strategy", mode: server.DeploymentModeManaged, env: withDefaultStrategy(" \t"),
			wantContains: []string{envDefaultStrategy, "hmm, hmm_embedding"},
		},
		{
			name: "managed serving worker with cluster default", mode: server.DeploymentModeManaged, env: withDefaultStrategy("cluster"),
			wantContains: []string{envDefaultStrategy, `"cluster"`, "hmm, hmm_embedding"},
		},
		{
			name: "managed serving worker with retired beta default", mode: server.DeploymentModeManaged, env: withDefaultStrategy("hmm_beta"),
			wantContains: []string{envDefaultStrategy, `"hmm_beta"`},
		},
		{
			name: "managed serving worker with unregistered default", mode: server.DeploymentModeManaged, env: withDefaultStrategy("rl"),
			wantContains: []string{envDefaultStrategy, `"rl"`},
		},
		// Only a serving worker (assertion key set) registers the managed
		// policy strategies; legacy managed workers keep the cluster default.
		{
			name: "legacy managed worker with cluster default", mode: server.DeploymentModeManaged,
			env: map[string]string{"ROUTER_POLICY_ENVIRONMENT": "prod", envDefaultStrategy: "cluster"},
		},
		{
			name: "self-hosted serving env without default strategy", mode: server.DeploymentModeSelfHosted,
			env: withoutDefaultStrategy,
		},
		{
			name: "managed serving worker without key", mode: server.DeploymentModeManaged, env: fullServingEnv,
			wantContains: []string{envServingAssertionKey, "ROUTER_SERVING_TARGET", "ROUTER_SERVING_SELECTION_SET_URI"},
		},
		{
			name: "managed serving worker with only whitespace key", mode: server.DeploymentModeManaged,
			env:          map[string]string{envServingAssertionKey: " \t\n", "ROUTER_SERVING_TARGET": "prod-weave-internal"},
			wantContains: []string{envServingAssertionKey, "ROUTER_SERVING_TARGET"},
		},
		// A stamp injected as an empty value is still a stamped revision:
		// presence, not value, decides whether the key is required.
		{
			name: "managed serving worker with empty stamp value", mode: server.DeploymentModeManaged,
			env:          map[string]string{"ROUTER_SERVING_TARGET": "", "ROUTER_SERVING_IMAGE_DIGEST": " "},
			wantContains: []string{envServingAssertionKey, "ROUTER_SERVING_TARGET", "ROUTER_SERVING_IMAGE_DIGEST"},
		},
		{
			name: "legacy managed worker with policy environment only", mode: server.DeploymentModeManaged,
			env: map[string]string{"ROUTER_POLICY_ENVIRONMENT": "prod"},
		},
		// scripts/legacy_runtime_check boots this shape: an HMM-less managed
		// worker where billing gates inference.
		{name: "managed worker with no serving stamping", mode: server.DeploymentModeManaged, env: nil},
		{name: "self-hosted with nothing set", mode: server.DeploymentModeSelfHosted, env: nil},
		// Self-hosted never wires managed serving: main.go only builds the
		// serving runtime when the assertion key is set, independent of mode.
		{name: "self-hosted with serving env minus key", mode: server.DeploymentModeSelfHosted, env: fullServingEnv},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateManagedServingBoot(tc.mode, func(key string) (string, bool) {
				value, ok := tc.env[key]
				return value, ok
			})
			if len(tc.wantContains) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tc.wantContains {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestValidateManagedServingBootRejectsEveryStampAlone(t *testing.T) {
	for _, name := range managedServingEnvVars {
		t.Run(name, func(t *testing.T) {
			err := validateManagedServingBoot(server.DeploymentModeManaged, func(key string) (string, bool) {
				if key != name {
					return "", false
				}
				return "stamped", true
			})
			require.ErrorContains(t, err, envServingAssertionKey)
			assert.ErrorContains(t, err, name)
		})
	}
}

func TestManagedServingWeakOptInFailsBeforeDependencies(t *testing.T) {
	t.Setenv("ROUTER_SERVING_ASSERTION_KEY", "weak-key")
	t.Setenv("ROUTER_SERVING_TARGET", "")
	t.Setenv("ROUTER_SERVING_REGISTRY_URI", "invalid-registry")
	assert.True(t, managedServingEnabled())
	admission, snapshot, closeRegistry, err := buildManagedServingRuntime(context.Background(), nil)
	require.ErrorContains(t, err, "at least 32 key bytes")
	assert.Nil(t, admission)
	assert.Nil(t, snapshot)
	assert.Nil(t, closeRegistry)
}

func TestManagedServingStrategiesMatchRuntimeRegistration(t *testing.T) {
	for _, strategy := range managedServingStrategies {
		assert.True(t, router.IsHMMStrategy(strategy), "boot gate must only admit sidecar-backed policy strategies: %q", strategy)
	}
	assert.NotContains(t, managedServingStrategies, router.StrategyCluster)
	assert.NotContains(t, managedServingStrategies, router.StrategyHMMBeta)
}
