package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
)

func TestAtomicClassifierStartupIsOptInAndFailsClosed(t *testing.T) {
	t.Setenv("ROUTER_LLM_CLASSIFIER_CONFIG", "")
	require.NoError(t, configureAtomicClassifier(nil, nil, nil))
	t.Setenv("ROUTER_SERVING_ASSERTION_KEY", "managed")
	t.Setenv("ROUTER_LLM_CLASSIFIER_CONFIG", "/must-not-read")
	require.ErrorContains(t, configureAtomicClassifier(nil, nil, nil), "managed Modal release integration")
	t.Setenv("ROUTER_SERVING_ASSERTION_KEY", "")
	t.Setenv("ROUTER_LLM_CLASSIFIER_BEARER", strings.Repeat("b", 32))
	t.Setenv("ROUTER_LLM_CLASSIFIER_SIGNING_KEY", strings.Repeat("s", 32))
	model, found := catalog.ByID(catalog.ModelIDGPT55.String())
	require.True(t, found)
	arm := armid.ForModel(model)
	escalationClasses := []string{string(escalation.Low), string(escalation.Medium), string(escalation.High), string(escalation.Maximum)}
	roster := rosterdata.Roster{SchemaVersion: rosterdata.SchemaVersionPolicyV1, ClassOrder: escalationClasses, Clusters: map[string]rosterdata.Cluster{}, Ranking: rosterdata.Ranking{Alpha: map[string]float64{}, AlphaMin: map[string]float64{}, AlphaMax: map[string]float64{}, QualityBiasNeutral: 0.5, WIIScoreVersion: "fixture", WPIScoreVersion: "fixture", WIINormalizationSHA256: strings.Repeat("a", 64), WPINormalizationSHA256: strings.Repeat("a", 64)}}
	for _, class := range escalationClasses {
		roster.Clusters[class] = rosterdata.Cluster{ComplexityLabel: class, Arms: []string{arm}, ArmScores: map[string]float64{arm: 1}, ArmIndices: map[string]rosterdata.ArmIndices{arm: {WII: 50, WPI: 50}}, CostRefUSD: 1, LatencyRefMS: 1}
		roster.Ranking.Alpha[class], roster.Ranking.AlphaMin[class], roster.Ranking.AlphaMax[class] = 0.5, 0, 1
	}
	rosterBytes, err := json.Marshal(roster)
	require.NoError(t, err)
	rosterPath := filepath.Join(t.TempDir(), "policy.json")
	require.NoError(t, os.WriteFile(rosterPath, rosterBytes, 0600))
	config := atomicClassifierConfig{Release: "llm-classifier-v1.0.0", ReleaseSHA256: strings.Repeat("a", 64), Endpoint: "https://classifier.example.test", SelectionPolicyFile: rosterPath, SelectionPolicySHA256: fmt.Sprintf("%x", sha256.Sum256(rosterBytes)), InstallationIDs: []uuid.UUID{uuid.New()}}
	for _, test := range []struct {
		name      string
		mutate    func(*atomicClassifierConfig)
		wantError bool
	}{
		{"valid", func(*atomicClassifierConfig) {}, false},
		{"empty_allowlist", func(c *atomicClassifierConfig) { c.InstallationIDs = nil }, true},
		{"zero_installation", func(c *atomicClassifierConfig) { c.InstallationIDs = []uuid.UUID{uuid.Nil} }, true},
		{"mutable_release", func(c *atomicClassifierConfig) { c.Release = "latest" }, true},
		{"bad_artifact", func(c *atomicClassifierConfig) { c.ReleaseSHA256 = strings.Repeat("z", 64) }, true},
		{"policy_drift", func(c *atomicClassifierConfig) { c.SelectionPolicySHA256 = strings.Repeat("b", 64) }, true},
		{"plaintext_endpoint", func(c *atomicClassifierConfig) { c.Endpoint = "http://classifier.example.test" }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := config
			test.mutate(&candidate)
			configBytes, err := json.Marshal(candidate)
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "classifier.json")
			require.NoError(t, os.WriteFile(path, configBytes, 0600))
			t.Setenv("ROUTER_LLM_CLASSIFIER_CONFIG", path)
			service := &proxy.Service{}
			err = configureAtomicClassifier(service, nil, map[string]struct{}{model.PrimaryProvider(): {}})
			if test.wantError {
				require.Error(t, err)
				require.False(t, service.PolicyStrategyAvailable(router.StrategyLLMClassifier))
			} else {
				require.NoError(t, err)
				require.True(t, service.PolicyStrategyAvailable(router.StrategyLLMClassifier))
			}
		})
	}
}
