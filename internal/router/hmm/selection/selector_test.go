package selection_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
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

func TestSetSelectorServesThePinnedRoster(t *testing.T) {
	current := testRoster()
	current.SHA256 = "current-roster-sha"
	pinned := testRoster()
	pinned.SHA256 = "pinned-roster-sha"
	pinned.Clusters["low"] = rosterdata.Cluster{Arms: []string{"vendor-a/cheap", "vendor-b/cheap"}}
	selector := selection.SetSelector(rosterdata.NewSet(current, pinned))

	input := policy.SelectionInput{
		Harness:            "claude-code",
		RankedFallback:     []policy.PreviewGroup{{Group: "low", Probability: 1}},
		CandidateRosterIDs: []string{"vendor-a/cheap", "vendor-b/cheap"},
	}

	unpinned, err := selector(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "current-roster-sha", unpinned.RosterSHA256, "no pin selects from the default roster")
	assert.Equal(t, "vendor-b/cheap", unpinned.Arm)

	input.RosterSHA256 = "pinned-roster-sha"
	got, err := selector(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "pinned-roster-sha", got.RosterSHA256, "the pick must report the roster that chose the arm")
	assert.Equal(t, "vendor-a/cheap", got.Arm, "the pinned roster's order must decide the pick")
}

func TestSetSelectorFailsClosedOnUnknownRosterSHA(t *testing.T) {
	current := testRoster()
	current.SHA256 = "current-roster-sha"
	selector := selection.SetSelector(rosterdata.NewSet(current))

	_, err := selector(context.Background(), policy.SelectionInput{
		Harness:            "claude-code",
		RosterSHA256:       "not-loaded",
		RankedFallback:     []policy.PreviewGroup{{Group: "low", Probability: 1}},
		CandidateRosterIDs: []string{"vendor-a/cheap"},
	})

	assert.ErrorIs(t, err, router.ErrPolicyPinUnavailable, "an unloaded roster must never fall through to the current one")
}
