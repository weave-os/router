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
			providers.ProviderAnthropic: {"medium": "anthropic/sonnet"},
			providers.ProviderOpenAI:    {"medium": "openai/terra"},
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
	everyArm := candidateSet("openai/luna", "anthropic/haiku", "openai/sol", "anthropic/sonnet", "openai/terra")
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
		{name: "neither drops the claude code harness order", harness: "claude_code", candidates: everyArm, wantArm: "openai/luna"},
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
