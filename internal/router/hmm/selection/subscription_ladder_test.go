package selection_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
)

func ladderRoster() *rosterdata.Roster {
	return &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionV6,
		SubscriptionLadders: map[string]map[string]string{
			providers.ProviderAnthropic:    {"medium": "anthropic/sonnet"},
			providers.ProviderOpenAI:       {"medium": "openai/terra"},
			selection.NoSubscriptionLadder: {"medium": "xiaomi/mimo"},
		},
		Clusters: map[string]rosterdata.Cluster{
			"medium": {
				Arms: []string{"openai/luna", "anthropic/haiku", "openai/sol"},
				ArmsByHarness: map[rosterdata.Harness][]string{
					rosterdata.HarnessClaudeCode: {"anthropic/haiku", "openai/luna", "openai/sol"},
					rosterdata.HarnessCodex:      {"openai/luna", "anthropic/haiku", "openai/sol"},
				},
			},
		},
	}
}

func TestSubscriptionLadderLeadsTheGroupForTheSubscriptionInPlay(t *testing.T) {
	everyArm := candidateSet("openai/luna", "anthropic/haiku", "openai/sol", "anthropic/sonnet", "openai/terra", "xiaomi/mimo")
	both := []string{providers.ProviderAnthropic, providers.ProviderOpenAI}
	for _, tc := range []struct {
		name                  string
		harness               string
		subscriptionProviders []string
		candidates            map[string]struct{}
		wantArm               string
	}{
		{name: "claude only in claude code serves sonnet", harness: "claude_code", subscriptionProviders: []string{providers.ProviderAnthropic}, candidates: everyArm, wantArm: "anthropic/sonnet"},
		{name: "claude only in codex serves sonnet", harness: "codex", subscriptionProviders: []string{providers.ProviderAnthropic}, candidates: everyArm, wantArm: "anthropic/sonnet"},
		{name: "codex only in claude code serves terra", harness: "claude_code", subscriptionProviders: []string{providers.ProviderOpenAI}, candidates: everyArm, wantArm: "openai/terra"},
		{name: "both in claude code serves the claude ladder", harness: "claude_code", subscriptionProviders: both, candidates: everyArm, wantArm: "anthropic/sonnet"},
		{name: "both in codex serves the codex ladder", harness: "codex", subscriptionProviders: both, candidates: everyArm, wantArm: "openai/terra"},
		{name: "both in an unknown harness keeps the harness order", harness: "pi", subscriptionProviders: both, candidates: everyArm, wantArm: "openai/luna"},
		{name: "neither serves the no-subscription ladder", harness: "claude_code", candidates: everyArm, wantArm: "xiaomi/mimo"},
		{name: "neither with an ineligible ladder arm keeps the claude code order", harness: "claude_code", candidates: candidateSet("openai/luna", "anthropic/haiku"), wantArm: "anthropic/haiku"},
		{name: "neither with an ineligible ladder arm keeps the codex order", harness: "codex", candidates: candidateSet("openai/luna", "anthropic/haiku"), wantArm: "openai/luna"},
		{name: "an ineligible ladder arm falls back to the harness order", harness: "claude_code", subscriptionProviders: []string{providers.ProviderAnthropic}, candidates: candidateSet("openai/luna", "anthropic/haiku"), wantArm: "anthropic/haiku"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pick, _, _, _, ok := selection.SelectGroupsWithSubscriptions(ladderRoster(), []selection.Group{{Label: "medium"}}, tc.harness, tc.subscriptionProviders, tc.candidates, nil, nil, nil, nil)

			require.True(t, ok)
			assert.Equal(t, tc.wantArm, pick.Arm)
		})
	}
}

func TestRosterWithoutLaddersIgnoresSubscriptions(t *testing.T) {
	roster := ladderRoster()
	roster.SubscriptionLadders = nil

	pick, _, _, _, ok := selection.SelectGroupsWithSubscriptions(roster, []selection.Group{{Label: "medium"}}, "claude_code", nil, candidateSet("openai/luna", "anthropic/haiku"), nil, nil, nil, nil)

	require.True(t, ok)
	assert.Equal(t, "anthropic/haiku", pick.Arm)
}

func TestNoSubscriptionLadderServesEachGroupWithinTheHarness(t *testing.T) {
	haiku, luna, mimo := "anthropic/claude-haiku-5.5", "openai/gpt-6-luna", "xiaomi/mimo-v2.6-pro"
	solHigh, sol, opus, opusMedium := "openai/gpt-6.1-sol:high", "openai/gpt-6.1-sol", "anthropic/claude-opus-5.5", "anthropic/claude-opus-5.5:medium"
	roster := &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionV6,
		SubscriptionLadders: map[string]map[string]string{
			selection.NoSubscriptionLadder: {"medium": mimo, "high": solHigh, "maximum": opus},
		},
		Clusters: map[string]rosterdata.Cluster{
			"low": {
				Arms: []string{luna, haiku, mimo},
				ArmsByHarness: map[rosterdata.Harness][]string{
					rosterdata.HarnessClaudeCode: {haiku, luna, mimo},
					rosterdata.HarnessCodex:      {luna, haiku, mimo},
				},
			},
			"medium": {
				Arms: []string{haiku, mimo, solHigh, luna},
				ArmsByHarness: map[rosterdata.Harness][]string{
					rosterdata.HarnessClaudeCode: {haiku, mimo, solHigh, luna},
					rosterdata.HarnessCodex:      {luna, haiku, mimo, solHigh},
				},
			},
			"high": {
				Arms: []string{mimo, opusMedium, solHigh, opus},
				ArmsByHarness: map[rosterdata.Harness][]string{
					rosterdata.HarnessClaudeCode: {opus, mimo, opusMedium, solHigh},
					rosterdata.HarnessCodex:      {sol, mimo, opusMedium, solHigh},
				},
			},
			"maximum": {
				Arms: []string{opusMedium, opus, sol},
				ArmsByHarness: map[rosterdata.Harness][]string{
					rosterdata.HarnessClaudeCode: {opus, opusMedium, sol},
					rosterdata.HarnessCodex:      {sol, opusMedium, opus},
				},
			},
		},
	}
	everyArm := candidateSet(haiku, luna, mimo, solHigh, sol, opus, opusMedium)
	for _, tc := range []struct {
		harness, group, wantArm string
		candidates              map[string]struct{}
	}{
		{harness: "claude_code", group: "low", wantArm: haiku, candidates: everyArm},
		{harness: "codex", group: "low", wantArm: luna, candidates: everyArm},
		{harness: "claude_code", group: "medium", wantArm: mimo, candidates: everyArm},
		{harness: "codex", group: "medium", wantArm: mimo, candidates: everyArm},
		{harness: "claude_code", group: "high", wantArm: solHigh, candidates: everyArm},
		{harness: "codex", group: "high", wantArm: solHigh, candidates: everyArm},
		{harness: "claude_code", group: "maximum", wantArm: opus, candidates: everyArm},
		{harness: "codex", group: "maximum", wantArm: opus, candidates: everyArm},
		{harness: "codex", group: "maximum", wantArm: sol, candidates: candidateSet(sol, opusMedium)},
		{harness: "claude_code", group: "high", wantArm: opus, candidates: candidateSet(opus, mimo)},
	} {
		t.Run(tc.harness+"/"+tc.group+"/"+tc.wantArm, func(t *testing.T) {
			pick, _, _, _, ok := selection.SelectGroupsWithSubscriptions(roster, []selection.Group{{Label: tc.group}}, tc.harness, nil, tc.candidates, nil, nil, nil, nil)

			require.True(t, ok)
			assert.Equal(t, tc.wantArm, pick.Arm)
		})
	}
}
