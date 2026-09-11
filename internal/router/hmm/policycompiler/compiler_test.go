package policycompiler_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/policycompiler"
	"weave-os/router/internal/router/hmm/rosterdata"
)

func TestCompileLegacyRosterPreservesWIIDescription(t *testing.T) {
	source := []byte(`{
  "schema_version": "hmm_router_cluster_roster_v7",
  "ranking": {
    "wii_v1": "six-benchmark absolute WII with frozen normalization",
    "alpha": {"low": 0.4},
    "alpha_min": {"low": 0.05},
    "alpha_max": {"low": 0.8},
    "quality_bias_neutral": 0.7,
    "wii_score_version": "wii-v1",
    "wii_normalization_sha256": "wii-sha",
    "wpi_score_version": "wpi-v1",
    "wpi_normalization_sha256": "wpi-sha"
  },
  "clusters": {
    "low": {
      "complexity_label": "low",
      "arms": ["openai/gpt-5.6-sol"],
      "arms_by_harness": {"codex": ["openai/gpt-5.6-sol"]},
      "cost_ref_usd": 0.02,
      "latency_ref_ms": 8000,
      "arm_scores": {"openai/gpt-5.6-sol": 10},
      "arm_indices": {"openai/gpt-5.6-sol": {"wii_v1": 50, "wpi_v1": 10}}
    }
  },
  "manual_pins": {"pi": {"low": ["openai/gpt-5.6-sol"]}}
}`)

	canonical, policy, err := policycompiler.Compile(source, policycompiler.Options{
		ClassOrder: []string{"low"},
	})
	require.NoError(t, err)
	assert.Equal(t, rosterdata.SchemaVersionPolicyV1, policy.SchemaVersion)
	assert.Equal(t, "six-benchmark absolute WII with frozen normalization", policy.Ranking.WIIDescription)
	assert.Equal(t, []string{"openai/gpt-5.6-sol"}, policy.Clusters["low"].ManualPinsByHarness[rosterdata.HarnessPI])
	assert.Contains(t, string(canonical), `"wii_v1":"six-benchmark absolute WII with frozen normalization"`)
}

func TestCompileMapsPooledPinsToAllHarness(t *testing.T) {
	source := []byte(`{
  "schema_version": "hmm_router_cluster_roster_v7",
  "ranking": {
    "alpha": {"low": 0.4},
    "alpha_min": {"low": 0.05},
    "alpha_max": {"low": 0.8},
    "quality_bias_neutral": 0.7,
    "wii_score_version": "wii-v1",
    "wii_normalization_sha256": "wii-sha",
    "wpi_score_version": "wpi-v1",
    "wpi_normalization_sha256": "wpi-sha"
  },
  "clusters": {
    "low": {
      "complexity_label": "low",
      "arms": ["openai/gpt-5.6-sol"],
      "arms_by_harness": {"codex": ["openai/gpt-5.6-sol"]},
      "cost_ref_usd": 0.02,
      "latency_ref_ms": 8000,
      "arm_scores": {"openai/gpt-5.6-sol": 10},
      "arm_indices": {"openai/gpt-5.6-sol": {"wii_v1": 50, "wpi_v1": 10}}
    }
  },
  "manual_pins": {"pooled": {"low": ["x-ai/grok-4.6"]}}
}`)

	canonical, policy, err := policycompiler.Compile(source, policycompiler.Options{
		ClassOrder: []string{"low"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"x-ai/grok-4.6"}, policy.Clusters["low"].ManualPinsByHarness[rosterdata.HarnessAll])
	assert.NotContains(t, string(canonical), "pooled")
}
