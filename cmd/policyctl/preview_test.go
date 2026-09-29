package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
)

func previewRoster() *rosterdata.Roster {
	return &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionV7,
		ClassOrder:    []string{"low"},
		Ranking: rosterdata.Ranking{
			Alpha: map[string]float64{"low": 0.6}, AlphaMin: map[string]float64{"low": 0.1},
			AlphaMax: map[string]float64{"low": 0.9}, QualityBiasNeutral: 0.7,
			WIIScoreVersion: "fixture", WIINormalizationSHA256: "fixture",
			WPIScoreVersion: "fixture", WPINormalizationSHA256: "fixture",
		},
		Clusters: map[string]rosterdata.Cluster{
			"low": {
				ComplexityLabel: "low", Arms: []string{"openai/gpt-6-sol:high", "openai/gpt-6-luna"},
				ArmsByHarness: map[rosterdata.Harness][]string{rosterdata.HarnessPI: {"openai/gpt-6-luna", "openai/gpt-6-sol:high"}},
				CostRefUSD:    0.01, LatencyRefMS: 500,
				ArmScores: map[string]float64{"openai/gpt-6-sol:high": 20, "openai/gpt-6-luna": 10},
				ArmIndices: map[string]rosterdata.ArmIndices{
					"openai/gpt-6-sol:high": {WII: 90, WPI: 80},
					"openai/gpt-6-luna":     {WII: 60, WPI: 10},
				},
				ManualPinsByHarness: map[rosterdata.Harness][]string{rosterdata.HarnessCodex: {"openai/gpt-6-luna"}},
			},
		},
	}
}

func TestDraftPreviewAppliesCompiledSourcePreferences(t *testing.T) {
	preferenceCases := []struct {
		name            string
		configure       func(*rosterdata.Roster)
		manualPin       bool
		preferredVendor bool
	}{
		{
			name: "manual pin",
			configure: func(roster *rosterdata.Roster) {
				roster.ManualPins = map[string]map[string][]string{
					"codex": {"low": {"x-ai/grok-4.6"}},
				}
			},
			manualPin: true,
		},
		{
			name: "vendor priority",
			configure: func(roster *rosterdata.Roster) {
				roster.HarnessVendorPriority = map[rosterdata.Harness]rosterdata.HarnessVendorPriority{
					rosterdata.HarnessCodex: {Vendors: []string{"x-ai"}, Clusters: []string{"low"}},
				}
			},
			preferredVendor: true,
		},
	}

	for _, preferenceCase := range preferenceCases {
		t.Run(preferenceCase.name, func(t *testing.T) {
			roster := &rosterdata.Roster{
				SchemaVersion: rosterdata.SchemaVersionV7,
				ClassOrder:    []string{"low"},
				Ranking: rosterdata.Ranking{
					Alpha: map[string]float64{"low": 0.6}, AlphaMin: map[string]float64{"low": 0.1},
					AlphaMax: map[string]float64{"low": 0.9}, QualityBiasNeutral: 0.7,
					WIIScoreVersion: "fixture", WIINormalizationSHA256: "fixture",
					WPIScoreVersion: "fixture", WPINormalizationSHA256: "fixture",
				},
				Clusters: map[string]rosterdata.Cluster{"low": {
					ComplexityLabel: "low", Arms: []string{"openai/gpt-6-sol:high", "x-ai/grok-4.6"},
					CostRefUSD: 0.01, LatencyRefMS: 500,
					ArmScores: map[string]float64{"openai/gpt-6-sol:high": 20, "x-ai/grok-4.6": 10},
					ArmIndices: map[string]rosterdata.ArmIndices{
						"openai/gpt-6-sol:high": {WII: 90, WPI: 80}, "x-ai/grok-4.6": {WII: 60, WPI: 10},
					},
				}},
			}
			preferenceCase.configure(roster)
			qualityBias := 1.0
			harness := string(rosterdata.HarnessCodex)

			preview, err := renderDraftPreview(roster, nil, "/draft.json", "staging-01", &harness, &qualityBias, nil, 0)
			require.NoError(t, err)
			assert.Equal(t, "x-ai/grok-4.6", preview.Clusters["low"].Arms[0].Arm)
			assert.Equal(t, preferenceCase.manualPin, preview.Clusters["low"].Arms[0].ManualPin)
			assert.Equal(t, preferenceCase.preferredVendor, preview.Clusters["low"].Arms[0].PreferredVendor)
		})
	}
}

func TestDynamicPreviewAllowsFullyGloballyPinnedClusterWithoutIndices(t *testing.T) {
	roster := previewRoster()
	cluster := roster.Clusters["low"]
	cluster.ArmIndices = nil
	cluster.ManualPinsByHarness = map[rosterdata.Harness][]string{
		rosterdata.HarnessAll: {"openai/gpt-6-luna", "openai/gpt-6-sol:high"},
	}
	roster.Clusters["low"] = cluster
	qualityBias := 1.0

	preview, err := renderDraftPreview(roster, nil, "/draft.json", "staging-01", nil, &qualityBias, nil, 0)
	require.NoError(t, err)
	require.Len(t, preview.Clusters["low"].Arms, 2)
	assert.Equal(t, "openai/gpt-6-luna", preview.Clusters["low"].Arms[0].Arm)
}

func TestDraftPreviewUsesServingSelectorOrder(t *testing.T) {
	roster := previewRoster()
	quality := 1.0
	preview, err := renderDraftPreview(roster, nil, "/draft.json", "staging-01", nil, &quality, nil, 3)
	require.NoError(t, err)
	assert.Equal(t, draftPreviewSchema, preview.PreviewSchema)
	assert.Equal(t, "openai/gpt-6-sol:high", preview.Clusters["low"].Arms[0].Arm)
	assert.Equal(t, "openai/gpt-6-luna", *preview.QualityGrid["low"][0].Winner)
	assert.Equal(t, "openai/gpt-6-sol:high", *preview.QualityGrid["low"][2].Winner)
	assert.Equal(t, float64(0.1), preview.QualityGrid["low"][0].Alpha)
	assert.Equal(t, float64(0.9), preview.QualityGrid["low"][2].Alpha)

	pick, err := selection.Selector(roster)(context.Background(), policy.SelectionInput{
		Unscorable: true, ForcedGroup: "low", QualityBias: &quality,
		CandidateRosterIDs: []string{"openai/gpt-6-sol", "openai/gpt-6-luna"},
	})
	require.NoError(t, err)
	assert.Equal(t, pick.Arm, preview.Clusters["low"].Arms[0].Arm)
	assert.Equal(t, pick.Trace.EffectiveOrders["low"], []string{preview.Clusters["low"].Arms[0].Arm, preview.Clusters["low"].Arms[1].Arm})
	assert.Equal(t, roundSix(float64(pick.ArmScoresByGroup["low"][pick.Arm])), *preview.Clusters["low"].Arms[0].Score)
}

func TestDraftPreviewStaticHarnessPinsAndAlphaOverride(t *testing.T) {
	roster := previewRoster()
	pi := string(rosterdata.HarnessPI)
	static, err := renderDraftPreview(roster, nil, "/draft.json", "", &pi, nil, nil, 0)
	require.NoError(t, err)
	assert.Nil(t, static.QualityBias)
	assert.Equal(t, "openai/gpt-6-luna", static.Clusters["low"].Arms[0].Arm)
	assert.Equal(t, 1, static.Clusters["low"].Arms[0].OriginalRank)
	assert.Equal(t, 10.0, *static.Clusters["low"].Arms[0].Score)

	codex := string(rosterdata.HarnessCodex)
	quality := 1.0
	pinned, err := renderDraftPreview(roster, nil, "/draft.json", "", &codex, &quality, nil, 0)
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-6-luna", pinned.Clusters["low"].Arms[0].Arm)
	assert.True(t, pinned.Clusters["low"].Arms[0].ManualPin)

	overridden, err := renderDraftPreview(roster, nil, "/draft.json", "", nil, nil, map[string]float64{"low": 0.1}, 0)
	require.NoError(t, err)
	assert.Nil(t, overridden.QualityBias)
	assert.Nil(t, overridden.Clusters["low"].QualityBias)
	assert.Equal(t, 0.1, overridden.Clusters["low"].Alpha)
	assert.Equal(t, "openai/gpt-6-luna", overridden.Clusters["low"].Arms[0].Arm)
}

func TestRunPreviewUsesExplicitFiveClassOrderWithoutRegistry(t *testing.T) {
	roster := previewRoster()
	roster.SchemaVersion = rosterdata.SchemaVersionV75C
	roster.ClassOrder = nil
	cluster := roster.Clusters["low"]
	labels := []string{"fast", "explore", "balanced", "high", "maximum"}
	roster.Clusters = make(map[string]rosterdata.Cluster, len(labels))
	roster.Ranking.Alpha = make(map[string]float64, len(labels))
	roster.Ranking.AlphaMin = make(map[string]float64, len(labels))
	roster.Ranking.AlphaMax = make(map[string]float64, len(labels))
	for _, label := range labels {
		cluster.ComplexityLabel = label
		roster.Clusters[label] = cluster
		roster.Ranking.Alpha[label] = 0.6
		roster.Ranking.AlphaMin[label] = 0.1
		roster.Ranking.AlphaMax[label] = 0.9
	}
	payload, err := json.Marshal(roster)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "draft.json")
	require.NoError(t, os.WriteFile(path, payload, 0o644))
	output, err := os.CreateTemp(t.TempDir(), "preview-*.json")
	require.NoError(t, err)
	stdout := os.Stdout
	os.Stdout = output
	t.Cleanup(func() { os.Stdout = stdout })
	assert.ErrorContains(t, runPreview([]string{"--roster-file", path}), "class order is required")
	runErr := runPreview([]string{"--roster-file", path, "--class-order", strings.Join(labels, ","), "--environment", "staging-01", "--harness", "pi", "--grid", "2"})
	os.Stdout = stdout
	require.NoError(t, output.Close())
	require.NoError(t, runErr)
	encoded, err := os.ReadFile(output.Name())
	require.NoError(t, err)
	var preview draftPreview
	require.NoError(t, json.Unmarshal(encoded, &preview))
	assert.Equal(t, path, preview.Source)
	assert.Equal(t, "staging-01", preview.Environment)
	assert.Len(t, preview.Clusters, len(labels))
	assert.Len(t, preview.QualityGrid["fast"], 2)
	assert.Equal(t, string(rosterdata.HarnessPI), *preview.Harness)
}

func TestRunPreviewRejectsInvalidInputs(t *testing.T) {
	assert.ErrorContains(t, runPreview([]string{"--grid", "1"}), "--roster-file")
	assert.ErrorContains(t, runPreview([]string{"--roster-file", "draft.json", "--grid", "102"}), "--grid")
	assert.ErrorContains(t, runPreview([]string{"--roster-file", "draft.json", "--harness", "unknown"}), "unknown harness")
	assert.ErrorContains(t, runPreview([]string{"--roster-file", "draft.json", "--quality-bias", "NaN"}), "finite number")
	assert.ErrorContains(t, runPreview([]string{"--roster-file", "draft.json", "--grid", "3", "--alpha", "low=0.4"}), "cannot be combined")
}
