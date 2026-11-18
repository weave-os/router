package selection

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
)

func ladderRoster() *rosterdata.Roster {
	return &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionPolicyV1,
		SubscriptionLadders: map[string]map[rosterdata.Group]string{
			providers.ProviderAnthropic:        {"medium": "anthropic/sonnet"},
			providers.ProviderOpenAI:           {"medium": "openai/terra"},
			rosterdata.NoSubscriptionLadderKey: {"medium": "xiaomi/mimo"},
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
		{name: "claude only in codex keeps the codex order", harness: "codex", subscriptionProviders: []string{providers.ProviderAnthropic}, candidates: everyArm, wantArm: "openai/luna"},
		{name: "codex only in claude code keeps the claude code order", harness: "claude_code", subscriptionProviders: []string{providers.ProviderOpenAI}, candidates: everyArm, wantArm: "anthropic/haiku"},
		{name: "codex only in hyphenated claude code keeps the claude code order", harness: "claude-code", subscriptionProviders: []string{providers.ProviderOpenAI}, candidates: everyArm, wantArm: "anthropic/haiku"},
		{name: "claude only in an unknown harness serves sonnet", harness: "pi", subscriptionProviders: []string{providers.ProviderAnthropic}, candidates: everyArm, wantArm: "anthropic/sonnet"},
		{name: "both in claude code serves the claude ladder", harness: "claude_code", subscriptionProviders: both, candidates: everyArm, wantArm: "anthropic/sonnet"},
		{name: "both in codex serves the codex ladder", harness: "codex", subscriptionProviders: both, candidates: everyArm, wantArm: "openai/terra"},
		{name: "both in an unknown harness keeps the harness order", harness: "pi", subscriptionProviders: both, candidates: everyArm, wantArm: "openai/luna"},
		{name: "neither serves the no-subscription ladder", harness: "claude_code", candidates: everyArm, wantArm: "xiaomi/mimo"},
		{name: "neither with an ineligible ladder arm keeps the claude code order", harness: "claude_code", candidates: candidateSet("openai/luna", "anthropic/haiku"), wantArm: "anthropic/haiku"},
		{name: "neither with an ineligible ladder arm keeps the codex order", harness: "codex", candidates: candidateSet("openai/luna", "anthropic/haiku"), wantArm: "openai/luna"},
		{name: "an ineligible ladder arm falls back to the harness order", harness: "claude_code", subscriptionProviders: []string{providers.ProviderAnthropic}, candidates: candidateSet("openai/luna", "anthropic/haiku"), wantArm: "anthropic/haiku"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pick, _, _, _, ok := selectGroups(ladderRoster(), armid.CatalogIDForRoster, Request{Groups: []rosterdata.Group{"medium"}, Harness: tc.harness, LadderKey: SubscriptionLadder(tc.harness, tc.subscriptionProviders), Candidates: tc.candidates}).tuple()

			require.True(t, ok)
			assert.Equal(t, tc.wantArm, pick.Arm.String())
		})
	}
}

func TestRosterWithoutLaddersIgnoresSubscriptions(t *testing.T) {
	roster := ladderRoster()
	roster.SubscriptionLadders = nil

	pick, _, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: []rosterdata.Group{"medium"}, Harness: "claude_code", LadderKey: SubscriptionLadder("claude_code", nil), Candidates: candidateSet("openai/luna", "anthropic/haiku")}).tuple()

	require.True(t, ok)
	assert.Equal(t, "anthropic/haiku", pick.Arm.String())
}

func TestNoSubscriptionLadderServesEachGroupWithinTheHarness(t *testing.T) {
	haiku, luna, mimo := "anthropic/claude-haiku-5.5", "openai/gpt-6-luna", "xiaomi/mimo-v2.6-pro"
	solHigh, sol, opus, opusMedium := "openai/gpt-6.1-sol:high", "openai/gpt-6.1-sol", "anthropic/claude-opus-5.5", "anthropic/claude-opus-5.5:medium"
	roster := &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionPolicyV1,
		SubscriptionLadders: map[string]map[rosterdata.Group]string{
			rosterdata.NoSubscriptionLadderKey: {"medium": mimo, "high": solHigh},
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
		{harness: "codex", group: "maximum", wantArm: sol, candidates: everyArm},
		{harness: "codex", group: "maximum", wantArm: opusMedium, candidates: candidateSet(opus, opusMedium)},
		{harness: "claude_code", group: "high", wantArm: opus, candidates: candidateSet(opus, mimo)},
	} {
		t.Run(tc.harness+"/"+tc.group+"/"+tc.wantArm, func(t *testing.T) {
			pick, _, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: []rosterdata.Group{rosterdata.Group(tc.group)}, Harness: tc.harness, LadderKey: SubscriptionLadder(tc.harness, nil), Candidates: tc.candidates}).tuple()

			require.True(t, ok)
			assert.Equal(t, tc.wantArm, pick.Arm.String())
		})
	}
}

func TestSubscriptionLadderNamesTheLadderInPlay(t *testing.T) {
	both := []string{providers.ProviderAnthropic, providers.ProviderOpenAI}
	for _, tc := range []struct {
		name                  string
		harness               string
		subscriptionProviders []string
		want                  string
	}{
		{name: "no subscription", harness: "claude_code", want: rosterdata.NoSubscriptionLadderKey},
		{name: "native subscription", harness: "codex", subscriptionProviders: both, want: providers.ProviderOpenAI},
		{name: "foreign subscription in a native harness", harness: "codex", subscriptionProviders: []string{providers.ProviderAnthropic}, want: ""},
		{name: "single subscription in an unknown harness", harness: "pi", subscriptionProviders: []string{providers.ProviderAnthropic}, want: providers.ProviderAnthropic},
		{name: "several subscriptions in an unknown harness", harness: "pi", subscriptionProviders: both, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SubscriptionLadder(tc.harness, tc.subscriptionProviders))
		})
	}
}

func TestLadderLeadMayComeFromOutsideTheGroup(t *testing.T) {
	roster := ladderRoster()
	require.NotContains(t, roster.Clusters["medium"].Arms, "anthropic/sonnet")

	pick, _, _, _, ok := selectGroups(roster, armid.CatalogIDForRoster, Request{Groups: []rosterdata.Group{"medium"}, Harness: "claude_code", LadderKey: SubscriptionLadder("claude_code", []string{providers.ProviderAnthropic}), Candidates: candidateSet("anthropic/sonnet", "anthropic/haiku")}).tuple()

	require.True(t, ok)
	assert.Equal(t, rosterdata.Group("medium"), pick.Group)
	assert.Equal(t, "anthropic/sonnet", pick.Arm.String())
}
