package selection_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/taskdomain"
)

func TestLiveSelectorTaskCorrectionPreservesComplexityAndEligibility(t *testing.T) {
	roster := dynamicRoster()
	roster.ClassOrder = []string{"low", "balanced", "high", "maximum"}
	for _, group := range roster.ClassOrder[1:] {
		roster.Clusters[group] = roster.Clusters["low"]
		roster.Ranking.Alpha[group] = roster.Ranking.Alpha["low"]
		roster.Ranking.AlphaMin[group] = roster.Ranking.AlphaMin["low"]
		roster.Ranking.AlphaMax[group] = roster.Ranking.AlphaMax["low"]
	}
	evidence := domainEvidenceForTest(t, roster)
	digest := strings.Repeat("e", 64)
	selector := selection.SelectorWithDomainEvidence(roster, evidence, digest)
	input := policy.SelectionInput{ClassOrder: roster.ClassOrder, ClassProbabilities: map[string]float64{"low": .7, "balanced": .1, "high": .15, "maximum": .05}, CandidateRosterIDs: []string{"vendor-a/quality", "vendor-b/cheap"}}
	baseline, err := selector(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "vendor-a/quality", baseline.Arm)
	input.TaskDomain = &taskdomain.Outcome{Status: taskdomain.Ready, EvidenceSHA256: digest, Profile: fullProfile(selection.DomainInfra)}
	adjusted, err := selector(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "vendor-b/cheap", adjusted.Arm)
	assert.Equal(t, baseline.Group, adjusted.Group)
	assert.Equal(t, baseline.Trace.ClassifierRanking, adjusted.Trace.ClassifierRanking)
	assert.Equal(t, []string{"low", "high", "balanced", "maximum"}, adjusted.Trace.ClassifierRanking)
	for index, fallback := range adjusted.RankedFallback {
		assert.Equal(t, baseline.RankedFallback[index].Probability, fallback.Probability)
		assert.Equal(t, baseline.RankedFallback[index].RosterArms, fallback.RosterArms)
	}
	assert.Equal(t, baseline.RankedFallback[0].Probability, adjusted.RankedFallback[0].Probability)
	assert.Equal(t, baseline.RankedFallback[0].RosterArms, adjusted.RankedFallback[0].RosterArms)
	components := adjusted.Trace.ScoreComponentsByGroup["low"]["vendor-a/quality"]
	assert.InDelta(t, 30, components.BaseScore, 1e-6)
	// Infra weights Terminal-Bench 4.0 at 0.6 and ITBench at 0.3, both scored 0 against a WII of 90.
	assert.InDelta(t, -4.86, components.TaskDomainCorrection, 1e-5)
	assert.InDelta(t, 25.14, components.TotalScore, 1e-5)
	input.CandidateRosterIDs = []string{"vendor-a/quality"}
	eligible, err := selector(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, baseline.Arm, eligible.Arm)
	input.CandidateRosterIDs = []string{"vendor-a/quality", "vendor-b/cheap"}
	for _, status := range []taskdomain.Status{taskdomain.Unavailable, taskdomain.TimedOut, taskdomain.NoTask} {
		input.TaskDomain.Status = status
		fallback, err := selector(context.Background(), input)
		require.NoError(t, err)
		assert.Equal(t, baseline.Arm, fallback.Arm)
	}
	input.TaskDomain.Status = taskdomain.Ready
	input.TaskDomain.EvidenceSHA256 = strings.Repeat("f", 64)
	stale, err := selector(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, baseline.Arm, stale.Arm)
}
