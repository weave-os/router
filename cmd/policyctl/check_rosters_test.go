package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/policycompiler"
	"weave-os/router/internal/router/hmm/rosterdata"
)

var standardTaxonomy = []string{"low", "medium", "high", "maximum"}

// v7RosterSource renders a reviewed v7 roster over the standard taxonomy with
// one arm per cluster, so the compiler needs no explicit class order.
func v7RosterSource(arm string, manualPins string) string {
	clusters := make([]string, 0, len(standardTaxonomy))
	for _, label := range standardTaxonomy {
		clusters = append(clusters, fmt.Sprintf(`"%[1]s": {
      "complexity_label": "%[1]s",
      "arms": ["%[2]s"],
      "arms_by_harness": {"codex": ["%[2]s"]},
      "cost_ref_usd": 0.02,
      "latency_ref_ms": 8000,
      "arm_scores": {"%[2]s": 10},
      "arm_indices": {"%[2]s": {"wii_v1": 50, "wpi_v1": 10}}
    }`, label, arm))
	}
	return fmt.Sprintf(`{
  "schema_version": "hmm_router_cluster_roster_v7",
  "ranking": {
    "alpha": {"low": 0.4, "medium": 0.6, "high": 0.8, "maximum": 0.95},
    "alpha_min": {"low": 0.05, "medium": 0.05, "high": 0.05, "maximum": 0.05},
    "alpha_max": {"low": 0.8, "medium": 0.9, "high": 0.95, "maximum": 1.0},
    "quality_bias_neutral": 0.7,
    "wii_score_version": "wii-v1",
    "wii_normalization_sha256": "wii-sha",
    "wpi_score_version": "wpi-v1",
    "wpi_normalization_sha256": "wpi-sha"
  },
  "clusters": {%s},
  "manual_pins": %s
}`, strings.Join(clusters, ",\n    "), manualPins)
}

const retiredSchemaRoster = `{"schema_version": "hmm_router_cluster_roster_v5_5c", "clusters": {"low": {"arms": ["openai/gpt-5.6-sol"]}}}`

// A v6 roster parses under the serving loader but carries no WPI provenance,
// so it can never become a Go policy and must be skipped rather than failed.
const v6SchemaRoster = `{
  "schema_version": "hmm_router_cluster_roster_v6",
  "ranking": {"alpha": {"low": 0.4}},
  "clusters": {
    "low": {
      "complexity_label": "low",
      "arms": ["openai/gpt-5.6-sol"],
      "cost_ref_usd": 0.02,
      "latency_ref_ms": 8000,
      "arm_scores": {"openai/gpt-5.6-sol": 10}
    }
  }
}`

func writeRoster(t *testing.T, dir, name, payload string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(payload), 0o644))
}

func TestCheckRosterDirCompilesV7SourceAndSkipsPreGoSchemas(t *testing.T) {
	dir := t.TempDir()
	// Legacy top-level "pooled" pins are the exact shape production served
	// before the Go-owned cutover; they must keep compiling.
	writeRoster(t, dir, "roster_pooled.json", v7RosterSource("openai/gpt-5.6-sol", `{"pooled": {"low": ["x-ai/grok-4.6"]}}`))
	writeRoster(t, dir, "roster_retired.json", retiredSchemaRoster)
	writeRoster(t, dir, "roster_v6.json", v6SchemaRoster)

	report, err := checkRosterDir(dir, policycompiler.Options{})

	require.NoError(t, err)
	require.Len(t, report.Compiled, 1)
	assert.Equal(t, "roster_pooled.json", report.Compiled[0].File)
	assert.Equal(t, rosterdata.SchemaVersionV7, report.Compiled[0].SchemaVersion)
	assert.Len(t, report.Compiled[0].PolicySHA256, 64)
	require.Len(t, report.Skipped, 2)
	assert.Equal(t, "roster_retired.json", report.Skipped[0].File)
	assert.Equal(t, "roster_v6.json", report.Skipped[1].File)
	assert.Equal(t, rosterdata.SchemaVersionV6, report.Skipped[1].SchemaVersion)
	assert.Empty(t, report.Failed)
}

func TestCheckRosterDirFailsCompilableSourceWithUnknownArm(t *testing.T) {
	dir := t.TempDir()
	writeRoster(t, dir, "roster_unknown_arm.json", v7RosterSource("nobody/no-such-model", `{}`))

	report, err := checkRosterDir(dir, policycompiler.Options{})

	require.NoError(t, err)
	assert.Empty(t, report.Compiled)
	require.Len(t, report.Failed, 1)
	assert.Equal(t, "roster_unknown_arm.json", report.Failed[0].File)
	assert.Contains(t, report.Failed[0].Error, "nobody/no-such-model")

	assert.ErrorContains(t, runCheckRosters([]string{"--dir", dir}), "failed to compile")
}

func TestCheckRosterDirFailsUnparseableRoster(t *testing.T) {
	dir := t.TempDir()
	writeRoster(t, dir, "roster_ok.json", v7RosterSource("openai/gpt-5.6-sol", `{}`))
	writeRoster(t, dir, "roster_truncated.json", `{"schema_version": "v7", "clusters": {`)
	writeRoster(t, dir, "roster_unversioned.json", `{"clusters": {}}`)

	report, err := checkRosterDir(dir, policycompiler.Options{})

	require.NoError(t, err)
	require.Len(t, report.Compiled, 1)
	assert.Empty(t, report.Skipped)
	require.Len(t, report.Failed, 2)
	assert.Equal(t, "roster_truncated.json", report.Failed[0].File)
	assert.Contains(t, report.Failed[0].Error, "parse roster header")
	assert.Equal(t, "roster_unversioned.json", report.Failed[1].File)
	assert.Contains(t, report.Failed[1].Error, "missing schema_version")

	assert.ErrorContains(t, runCheckRosters([]string{"--dir", dir}), "2 roster(s) failed")
}

func TestCheckRosterDirRejectsEmptyDirectory(t *testing.T) {
	_, err := checkRosterDir(t.TempDir(), policycompiler.Options{})

	require.Error(t, err)
	assert.ErrorContains(t, err, "no roster JSON files")
}

func TestRunCheckRostersFailsWhenNothingIsCompilable(t *testing.T) {
	dir := t.TempDir()
	writeRoster(t, dir, "roster_retired.json", retiredSchemaRoster)

	err := runCheckRosters([]string{"--dir", dir})

	require.Error(t, err)
	assert.ErrorContains(t, err, "no roster under")
}
