package proxy_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
)

var savingsMeasuredFrom = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func savingsTurnAt(clientApp, servedModel string) proxy.SessionSavingsTurn {
	return proxy.SessionSavingsTurn{
		Timestamp:           savingsMeasuredFrom.Add(time.Hour),
		ClientApp:           clientApp,
		RequestedModel:      "claude-opus-5-5",
		DecisionModel:       servedModel,
		DecisionProvider:    providers.ProviderAnthropic,
		DecisionReason:      "hmm_policy",
		InputTokens:         1_000_000,
		OutputTokens:        100_000,
		ActualCostUSDMicros: 1_000_000,
	}
}

func opusEvidence(harness string) proxy.TokenEfficiencyEvidence {
	from := savingsMeasuredFrom
	return proxy.TokenEfficiencyEvidence{
		Ratios: []proxy.TokenEfficiencyRatio{{
			Harness: harness, BaselineModel: "claude-opus-5-5", ServedModel: "claude-sonnet-5",
			InputPoint: 0.4, InputLower: 0.5, OutputPoint: 0.7, OutputLower: 0.8,
			Validity: proxy.TokenEfficiencyValidity{From: &from},
		}},
		QualityFactors: []proxy.TokenEfficiencyQualityFactor{{
			Harness: harness, BaselineModel: "claude-opus-5-5",
			RouterPassRateLower: 0.5, BaselinePassRateUpper: 0.6,
		}},
	}
}

func TestComputeNormalizedSessionSavings(t *testing.T) {
	// Opus at $4/$20 per 1M: 1M input + 100k output = $4 + $2. Lower-bound
	// ratios 0.5/0.8 make that $3.60 of comparison work; the billed $1 scaled
	// by the 0.6/0.5 pass-rate penalty leaves $2.40 saved.
	t.Run("reprices a scorer turn against Opus with measured ratios and pass-rate penalty", func(t *testing.T) {
		savings, ok := proxy.ComputeNormalizedSessionSavings(
			[]proxy.SessionSavingsTurn{savingsTurnAt(proxy.ClientAppClaudeCode, "claude-sonnet-5")},
			opusEvidence(proxy.TokenEfficiencyHarnessClaudeCode))
		require.True(t, ok)
		require.Equal(t, proxy.NormalizedSessionSavings{BaselineModel: "claude-opus-5-5", BaselineLabel: "Opus 5.5", SavingsUSDMicros: 2_400_000}, savings)
	})

	t.Run("subscription, forced, utility, weave-alias, unmeasured and pre-measurement turns save nothing", func(t *testing.T) {
		subscription := savingsTurnAt(proxy.ClientAppClaudeCode, "claude-sonnet-5")
		subscription.SubscriptionServed = true
		forced := savingsTurnAt(proxy.ClientAppClaudeCode, "claude-sonnet-5")
		forced.DecisionReason = "user_forced:claude-sonnet-5"
		utility := savingsTurnAt(proxy.ClientAppClaudeCode, "claude-sonnet-5")
		utility.DecisionReason = "compaction_hard_pin"
		alias := savingsTurnAt(proxy.ClientAppClaudeCode, "claude-sonnet-5")
		alias.RequestedModel = "Weave"
		unmeasured := savingsTurnAt(proxy.ClientAppClaudeCode, "gemini-3-pro")
		early := savingsTurnAt(proxy.ClientAppClaudeCode, "claude-sonnet-5")
		early.Timestamp = savingsMeasuredFrom.Add(-time.Hour)

		savings, ok := proxy.ComputeNormalizedSessionSavings(
			[]proxy.SessionSavingsTurn{subscription, forced, utility, alias, unmeasured, early},
			opusEvidence(proxy.TokenEfficiencyHarnessClaudeCode))
		require.True(t, ok)
		require.Zero(t, savings.SavingsUSDMicros)
	})

	t.Run("Pi uses OpenCode cells without a pass-rate penalty", func(t *testing.T) {
		savings, ok := proxy.ComputeNormalizedSessionSavings(
			[]proxy.SessionSavingsTurn{savingsTurnAt("pi", "claude-sonnet-5")},
			opusEvidence(proxy.TokenEfficiencyHarnessOpenCode))
		require.True(t, ok)
		require.Equal(t, proxy.NormalizedSessionSavings{BaselineModel: "claude-opus-5-5", BaselineLabel: "Opus 5.5", SavingsUSDMicros: 2_600_000}, savings)
	})

	t.Run("a model measured in its own harness uses the point estimate, capped at 1", func(t *testing.T) {
		evidence := opusEvidence(proxy.TokenEfficiencyHarnessOpenCode)
		evidence.Ratios[0].Native = true
		evidence.Ratios[0].InputPoint = 1.3
		savings, ok := proxy.ComputeNormalizedSessionSavings(
			[]proxy.SessionSavingsTurn{savingsTurnAt(proxy.ClientAppOpencode, "claude-sonnet-5")}, evidence)
		require.True(t, ok)
		// $4 x 1 + $2 x 0.7 - $1.
		require.Equal(t, int64(4_400_000), savings.SavingsUSDMicros)
	})

	t.Run("warm-prefill cache creation reprices as a cache read", func(t *testing.T) {
		evidence := opusEvidence(proxy.TokenEfficiencyHarnessOpenCode)
		evidence.Ratios[0].Native = true
		evidence.Ratios[0].InputPoint = 1
		turn := savingsTurnAt(proxy.ClientAppOpencode, "claude-sonnet-5")
		turn.InputTokens, turn.OutputTokens, turn.ActualCostUSDMicros = 0, 0, 0
		turn.CacheCreationTokens, turn.BaselineWarmPrefillTokens = 1_000_000, 400_000
		savings, ok := proxy.ComputeNormalizedSessionSavings([]proxy.SessionSavingsTurn{turn}, evidence)
		require.True(t, ok)
		// 600k writes at 1.25 x $4 + 400k reads at 0.05 x $4.
		require.Equal(t, int64(3_080_000), savings.SavingsUSDMicros)
	})

	t.Run("a zero router pass-rate lower bound drops the turn", func(t *testing.T) {
		evidence := opusEvidence(proxy.TokenEfficiencyHarnessClaudeCode)
		evidence.QualityFactors[0].RouterPassRateLower = 0
		savings, ok := proxy.ComputeNormalizedSessionSavings(
			[]proxy.SessionSavingsTurn{savingsTurnAt(proxy.ClientAppClaudeCode, "claude-sonnet-5")}, evidence)
		require.True(t, ok)
		require.Zero(t, savings.SavingsUSDMicros)
	})

	t.Run("loses money when the served model costs more than the comparison work", func(t *testing.T) {
		turn := savingsTurnAt(proxy.ClientAppClaudeCode, "claude-sonnet-5")
		turn.ActualCostUSDMicros = 4_000_000
		savings, ok := proxy.ComputeNormalizedSessionSavings([]proxy.SessionSavingsTurn{turn}, opusEvidence(proxy.TokenEfficiencyHarnessClaudeCode))
		require.True(t, ok)
		require.Equal(t, int64(-1_200_000), savings.SavingsUSDMicros)
	})

	t.Run("an unmeasured comparison reports no savings", func(t *testing.T) {
		_, ok := proxy.ComputeNormalizedSessionSavings(
			[]proxy.SessionSavingsTurn{savingsTurnAt(proxy.ClientAppCodex, "gpt-5.5")},
			opusEvidence(proxy.TokenEfficiencyHarnessClaudeCode))
		require.False(t, ok)
	})
}
