package selection_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
)

func TestDomainPreviewMatchesServingSelection(t *testing.T) {
	roster := dynamicRoster()
	evidence := domainEvidenceForTest(t, roster)
	profile := fullProfile(selection.DomainInfra)
	preview, err := selection.PreviewDomainRanking(roster, evidence, "low", "", profile)
	require.NoError(t, err)

	pick, scores, _, _, ok := selection.SelectGroupsWithDomainPreferences(
		roster, []selection.Group{{Label: "low"}}, "", candidateSet("vendor-a/quality", "vendor-b/cheap"), nil, nil, evidence, profile,
	)
	require.True(t, ok)
	assert.Equal(t, pick.Arm, preview.EffectiveWinner)
	assert.Equal(t, "vendor-a/quality", preview.BaselineWinner)
	assert.Equal(t, 0.15, preview.Influence)
	quality, cheap := preview.Arms[0], preview.Arms[1]
	assert.Equal(t, []int{1, 2}, []int{quality.BaselineRank, quality.EffectiveRank})
	assert.Equal(t, []int{2, 1}, []int{cheap.BaselineRank, cheap.EffectiveRank})
	assert.Equal(t, scores["low"]["vendor-a/quality"], quality.AdjustedScore)
	assert.InDelta(t, -4.86, quality.Correction, 1e-5)
	total := 0.0
	for _, term := range quality.Benchmarks {
		total += term.Contribution
	}
	assert.InDelta(t, float64(quality.Correction), total, 1e-5)
	itbench := cheap.Benchmarks[len(cheap.Benchmarks)-1]
	assert.Equal(t, selection.BenchmarkITBench, itbench.Benchmark)
	assert.InDelta(t, 0.3, itbench.Weight, 1e-12)
	assert.Nil(t, itbench.Quality)
	assert.Zero(t, itbench.Contribution)
}

func TestDomainPreviewDocsOnlyKeepsBaseline(t *testing.T) {
	roster := dynamicRoster()
	preview, err := selection.PreviewDomainRanking(roster, domainEvidenceForTest(t, roster), "low", "", fullProfile(selection.DomainDocs))
	require.NoError(t, err)
	assert.Zero(t, preview.Influence)
	for _, arm := range preview.Arms {
		assert.Equal(t, arm.BaselineRank, arm.EffectiveRank)
		assert.Zero(t, arm.Correction)
		assert.Empty(t, arm.Benchmarks)
	}
}

func TestDomainPreviewReportsPinSuppressedLeader(t *testing.T) {
	roster := dynamicRoster()
	cluster := roster.Clusters["low"]
	cluster.ManualPinsByHarness = map[rosterdata.Harness][]string{rosterdata.HarnessPI: {"vendor-a/quality"}}
	roster.Clusters["low"] = cluster
	preview, err := selection.PreviewDomainRanking(roster, domainEvidenceForTest(t, roster), "low", "pi", fullProfile(selection.DomainInfra))
	require.NoError(t, err)
	assert.Equal(t, "vendor-b/cheap", preview.NumericWinner)
	assert.Equal(t, "vendor-a/quality", preview.EffectiveWinner)
	assert.True(t, preview.WinnerSuppressed)
}

func TestDomainPreviewRejectsUnboundEvidence(t *testing.T) {
	roster := dynamicRoster()
	evidence := domainEvidenceForTest(t, roster)
	_, err := selection.PreviewDomainRanking(roster, evidence, "absent", "", fullProfile(selection.DomainInfra))
	require.Error(t, err)
	evidence.RosterSHA256 = "f"
	_, err = selection.PreviewDomainRanking(roster, evidence, "low", "", fullProfile(selection.DomainInfra))
	require.Error(t, err)
}
