package selection

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

func TestSelectorReturnsDeterministicPick(t *testing.T) {
	selector := selector(testRoster(), armid.CatalogIDForRoster, nil, "")

	pick, err := selector(context.Background(), policy.SelectionInput{
		Harness:            "claude-code",
		ClassOrder:         []string{"low", "balanced"},
		ClassProbabilities: map[string]float64{"low": 0.7, "balanced": 0.3},
		CandidateRosterIDs: []string{"vendor-a/cheap", "vendor-b/cheap"},
	})

	require.NoError(t, err)
	assert.Equal(t, "low", pick.Group)
	assert.Equal(t, "vendor-b/cheap", pick.Arm, "harness-specific order must decide the pick")
}

func TestSelectorFailsClosedWithoutClassification(t *testing.T) {
	selector := selector(testRoster(), armid.CatalogIDForRoster, nil, "")

	_, err := selector(context.Background(), policy.SelectionInput{Harness: "claude-code"})

	require.ErrorIs(t, err, ErrInvalidClassifierOutput)
	assert.NotErrorIs(t, err, ErrNoEligibleArm, "a broken classifier is an outage, not an eligibility result")
}

func TestSelectorUnscorableUsesRosterAndDoesNotDowngradeEscalation(t *testing.T) {
	roster := testRoster()
	roster.ClassOrder = []string{"low", "high"}
	selector := selector(roster, armid.CatalogIDForRoster, nil, "")
	input := policy.SelectionInput{
		Unscorable:         true,
		PreferredRosterID:  "vendor-a/top",
		CandidateRosterIDs: []string{"vendor-a/cheap", "vendor-a/top"},
	}
	pick, err := selector(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "high", pick.Group)

	input.MinimumGroup = escalation.High
	input.CandidateRosterIDs = []string{"vendor-a/cheap"}
	_, err = selector(context.Background(), input)
	require.ErrorIs(t, err, ErrNoEligibleArm)
}

func TestSelectorFailsClosedWhenNoRankedGroupHoldsAnEligibleArm(t *testing.T) {
	selector := selector(testRoster(), armid.CatalogIDForRoster, nil, "")

	_, err := selector(context.Background(), policy.SelectionInput{
		Harness:            "codex",
		ClassOrder:         []string{"high"},
		ClassProbabilities: map[string]float64{"high": 1.0},
		CandidateRosterIDs: []string{"vendor-a/cheap"},
	})

	assert.ErrorIs(t, err, ErrNoEligibleArm)
}

func TestSelectorRejectsMismatchedClassOrder(t *testing.T) {
	roster := testRoster()
	roster.ClassOrder = []string{"low", "balanced", "high", "effort", "efforts"}
	selector := selector(roster, armid.CatalogIDForRoster, nil, "")
	input := policy.SelectionInput{
		ClassOrder:         []string{"high", "balanced", "low", "effort", "efforts"},
		ClassProbabilities: map[string]float64{"low": 1, "balanced": 0, "high": 0, "effort": 0, "efforts": 0},
		CandidateRosterIDs: []string{"vendor-a/cheap"},
	}

	_, err := selector(context.Background(), input)
	require.ErrorIs(t, err, ErrClassifierTaxonomyMismatch)
	require.NotErrorIs(t, err, ErrNoEligibleArm)

	input.ClassOrder = append([]string(nil), roster.ClassOrder...)
	pick, err := selector(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "low", pick.Group)
}

func TestSelectorKeepsCrossProviderCandidatesAndAppliesBoundedPreferences(t *testing.T) {
	roster := &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionPolicyV1,
		Preferences: rosterdata.PreferencePolicy{
			PreferredModelBonus: 0.5,
		},
		Clusters: map[string]rosterdata.Cluster{
			"high": {
				Arms: []string{
					"openai/gpt-5.6-sol",
					"x-ai/grok-4.6",
					"anthropic/claude-fable-5.1",
				},
				ArmScores: map[string]float64{
					"openai/gpt-5.6-sol":         30,
					"x-ai/grok-4.6":              29.8,
					"anthropic/claude-fable-5.1": 29.7,
				},
			},
		},
	}
	selector := selector(roster, armid.CatalogIDForRoster, nil, "")
	input := policy.SelectionInput{
		ClassOrder:         []string{"high"},
		ClassProbabilities: map[string]float64{"high": 1},
		CandidateRosterIDs: []string{
			"openai/gpt-5.6-sol",
			"x-ai/grok-4.6",
			"anthropic/claude-fable-5.1",
		},
		PreferredModels: []string{"grok-4.6"},
	}

	pick, err := selector(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "x-ai/grok-4.6", pick.Arm)
	assert.Equal(t, input.CandidateRosterIDs, pick.Trace.CandidateRosterIDs)
	assert.Equal(t, []string{"grok-4.6"}, pick.Trace.PreferredModels)
	assert.InDelta(t, 0.5, pick.Trace.ScoreComponentsByGroup["high"]["x-ai/grok-4.6"].PreferredModelBonus, 1e-6)

	cluster := roster.Clusters["high"]
	cluster.ArmScores["openai/gpt-5.6-sol"] = 31
	roster.Clusters["high"] = cluster
	pick, err = selector(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-5.6-sol", pick.Arm, "bounded model preference must not erase a clear score gap")
}
