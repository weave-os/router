package selection_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/router/hmm/selection"
	"workweave/router/internal/router/policy"
)

func TestOverrideReturnsDeterministicPick(t *testing.T) {
	override := selection.Override(testRoster())

	pick, ok := override(context.Background(), policy.SelectionObservation{
		Harness: "claude-code",
		RankedFallback: []policy.PreviewGroup{
			{Group: "low", Probability: 0.7},
			{Group: "balanced", Probability: 0.3},
		},
		CandidateRosterIDs: []string{"vendor-a/cheap", "vendor-b/cheap"},
	})

	require.True(t, ok)
	assert.Equal(t, "low", pick.Group)
	assert.Equal(t, "vendor-b/cheap", pick.Arm, "harness-specific order must decide the pick")
}

func TestOverrideFailsOpenWithoutRankedFallback(t *testing.T) {
	override := selection.Override(testRoster())

	_, ok := override(context.Background(), policy.SelectionObservation{Harness: "claude-code"})

	assert.False(t, ok)
}

func TestOverrideFailsOpenWhenNoRankedGroupHoldsAnEligibleArm(t *testing.T) {
	override := selection.Override(testRoster())

	_, ok := override(context.Background(), policy.SelectionObservation{
		Harness: "codex",
		RankedFallback: []policy.PreviewGroup{
			{Group: "high", Probability: 1.0},
		},
		CandidateRosterIDs: []string{"vendor-a/cheap"},
	})

	assert.False(t, ok)
}
