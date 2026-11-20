package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/rosterdata"
)

func TestRunCompileWritesCanonicalBytes(t *testing.T) {
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
  }
}`)
	tempDir := t.TempDir()
	sourcePath := filepath.Join(tempDir, "roster.json")
	outputPath := filepath.Join(tempDir, "policy.json")
	summaryPath := filepath.Join(tempDir, "summary.json")
	require.NoError(t, os.WriteFile(sourcePath, source, 0o600))

	summaryFile, err := os.Create(summaryPath)
	require.NoError(t, err)
	stdout := os.Stdout
	os.Stdout = summaryFile
	t.Cleanup(func() {
		os.Stdout = stdout
		_ = summaryFile.Close()
	})
	compileErr := runCompile([]string{
		"--source", sourcePath,
		"--output", outputPath,
		"--source-revision", "test-revision",
		"--class-order", "low",
	})
	os.Stdout = stdout
	require.NoError(t, summaryFile.Close())
	require.NoError(t, compileErr)

	payload, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	summaryPayload, err := os.ReadFile(summaryPath)
	require.NoError(t, err)
	var summary struct {
		PolicySHA256 string `json:"policy_sha256"`
	}
	require.NoError(t, json.Unmarshal(summaryPayload, &summary))
	policy, err := rosterdata.ParseValidated(payload)
	require.NoError(t, err)
	canonical, err := rosterdata.CanonicalBytes(policy)
	require.NoError(t, err)
	assert.Equal(t, canonical, payload)
	assert.Equal(t, policyregistry.Digest(payload), summary.PolicySHA256)
	assert.False(t, bytes.HasSuffix(payload, []byte{'\n'}))
}

func releaseWithClassifier() policyregistry.Release {
	return policyregistry.Release{Classifier: policyregistry.ClassifierIdentity{
		ArtifactID:    "hmm_router_sidecar_package_64f27f963b2da6f6",
		PackageSHA256: "64f27f963b2da6f6394d4677f466c6f89a53fbad857b33581bc8be2ed01ac066",
		ImageDigest:   "sha256:3de4d22f3b895ade91d7eb2f7377586b050260c93d5e016bcbfde52ef2c5dba2",
	}}
}
