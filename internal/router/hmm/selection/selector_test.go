package selection_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/router/hmm/selection"
	"workweave/router/internal/router/policy"
)

func TestSelectorReturnsDeterministicPick(t *testing.T) {
	selector := selection.Selector(testRoster())

	pick, err := selector(context.Background(), policy.SelectionInput{
		Harness: "claude-code",
		RankedFallback: []policy.PreviewGroup{
			{Group: "low", Probability: 0.7},
			{Group: "balanced", Probability: 0.3},
		},
		CandidateRosterIDs: []string{"vendor-a/cheap", "vendor-b/cheap"},
	})

	require.NoError(t, err)
	assert.Equal(t, "low", pick.Group)
	assert.Equal(t, "vendor-b/cheap", pick.Arm, "harness-specific order must decide the pick")
}

func TestSelectorFailsClosedWithoutRankedFallback(t *testing.T) {
	selector := selection.Selector(testRoster())

	_, err := selector(context.Background(), policy.SelectionInput{Harness: "claude-code"})

	assert.ErrorIs(t, err, selection.ErrNoEligibleArm)
}

func TestSelectorFailsClosedWhenNoRankedGroupHoldsAnEligibleArm(t *testing.T) {
	selector := selection.Selector(testRoster())

	_, err := selector(context.Background(), policy.SelectionInput{
		Harness: "codex",
		RankedFallback: []policy.PreviewGroup{
			{Group: "high", Probability: 1.0},
		},
		CandidateRosterIDs: []string{"vendor-a/cheap"},
	})

	assert.ErrorIs(t, err, selection.ErrNoEligibleArm)
}
