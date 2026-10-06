package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/sessionpin"
)

func priorTurnEndedAt(d time.Duration) time.Time {
	return time.Now().Add(-d)
}

// priorTurn is a previous turn whose 100k-token prompt the baseline would hold warm.
func priorTurn(model string, ago time.Duration) turnLoopResult {
	return turnLoopResult{PriorServedModel: model, PriorServedEndedAt: priorTurnEndedAt(ago), PriorPromptTokens: 100_000}
}

func TestBaselineWarmPrefillTokens_SwitchWithinBaselineTTLIsCorrected(t *testing.T) {
	turnResult := priorTurn("claude-opus-5", 2*time.Minute)

	assert.Equal(t, 100_000, turnResult.baselineWarmPrefillTokens(time.Now(), 100_000, 0, "gpt-5.6-sol", "claude-opus-5", false),
		"switching away from the baseline pays a cold prefill the baseline would have read warm")
}

func TestBaselineWarmPrefillTokens_SwitchBackToBaselineIsCorrected(t *testing.T) {
	turnResult := priorTurn("gpt-5.6-sol", 2*time.Minute)

	assert.Equal(t, 100_000, turnResult.baselineWarmPrefillTokens(time.Now(), 100_000, 0, "claude-opus-5", "claude-opus-5", false),
		"returning to the baseline re-primes a cache the baseline never let go cold")
}

// Content appended since the previous turn is a cache write for the baseline
// too; only the previous prompt, less what the served model did read, was warm.
func TestBaselineWarmPrefillTokens_LimitedToPreviousPrompt(t *testing.T) {
	turnResult := priorTurn("claude-opus-5", 2*time.Minute)

	assert.Equal(t, 100_000, turnResult.baselineWarmPrefillTokens(time.Now(), 110_000, 0, "gpt-5.6-sol", "claude-opus-5", false),
		"10k of new content stays a write")
	assert.Equal(t, 95_000, turnResult.baselineWarmPrefillTokens(time.Now(), 105_000, 5_000, "gpt-5.6-sol", "claude-opus-5", false),
		"a 5k system prefix the served model already read is not repriced again")
}

// An effort change keeps the base model, and a baseline that never switched
// carries the client's effort anyway, so it is not a router-caused prefill.
func TestBaselineWarmPrefillTokens_EffortOnlyChangeIsNotASwitch(t *testing.T) {
	turnResult := priorTurn("gpt-5.6-sol:low", time.Minute)

	assert.Zero(t, turnResult.baselineWarmPrefillTokens(time.Now(), 100_000, 0, "gpt-5.6-sol", "claude-opus-5", false))
}

// The TTL that matters is the baseline's: Anthropic holds the prefix for an
// hour, OpenAI for five minutes.
func TestBaselineWarmPrefillTokens_UsesBaselineProviderTTL(t *testing.T) {
	turnResult := priorTurn("gpt-5.6-sol", 20*time.Minute)
	assert.Equal(t, 100_000, turnResult.baselineWarmPrefillTokens(time.Now(), 100_000, 0, "claude-opus-5", "claude-opus-5", false),
		"a 20m gap is inside the Anthropic baseline's cache TTL")

	turnResult = priorTurn("claude-opus-5", 20*time.Minute)
	assert.Zero(t, turnResult.baselineWarmPrefillTokens(time.Now(), 100_000, 0, "gpt-5.6-sol", "gpt-5.6-sol", false),
		"a 20m gap expires the OpenAI baseline's cache, so it would have re-primed too")
}

// A fresh HMM switch has no thread pin; the prior served turn lives only on
// the HMM history pin, and its timing and size must come from that same pin.
func TestApplySwitchHistory_ReadsThePriorTurnFromTheSelectedPin(t *testing.T) {
	hmmHistory := sessionpin.Pin{
		LastServedModel:      "claude-opus-5",
		Provider:             providers.ProviderAnthropic,
		LastTurnEndedAt:      priorTurnEndedAt(2 * time.Minute),
		LastInputTokens:      2_000,
		LastCachedReadTokens: 98_000,
	}
	var turnResult turnLoopResult
	turnResult.applySwitchHistory(sessionpin.Pin{}, hmmHistory)

	assert.Equal(t, 100_000, turnResult.baselineWarmPrefillTokens(time.Now(), 110_000, 0, "gpt-5.6-sol", "claude-opus-5", false))
}

func TestBaselineWarmPrefillTokens_BaselineWouldAlsoBeCold(t *testing.T) {
	cases := map[string]struct {
		turnResult       turnLoopResult
		baseline         string
		historyTruncated bool
	}{
		"first turn":           {turnResult: turnLoopResult{PriorServedEndedAt: priorTurnEndedAt(time.Minute), PriorPromptTokens: 100_000}, baseline: "claude-opus-5"},
		"no prior turn time":   {turnResult: turnLoopResult{PriorServedModel: "claude-opus-5", PriorPromptTokens: 100_000}, baseline: "claude-opus-5"},
		"no prior prompt size": {turnResult: turnLoopResult{PriorServedModel: "claude-opus-5", PriorServedEndedAt: priorTurnEndedAt(time.Minute)}, baseline: "claude-opus-5"},
		"client trimmed":       {turnResult: turnLoopResult{PriorServedModel: "claude-opus-5", PriorServedEndedAt: priorTurnEndedAt(time.Minute), PriorPromptTokens: 100_000, PrefixTrimmed: true}, baseline: "claude-opus-5"},
		"ingress truncation":   {turnResult: priorTurn("claude-opus-5", time.Minute), baseline: "claude-opus-5", historyTruncated: true},
		"unpriced baseline":    {turnResult: priorTurn("claude-opus-5", time.Minute), baseline: "not-a-model"},
		"cache gap past TTL":   {turnResult: priorTurn("claude-opus-5", 2*time.Hour), baseline: "claude-opus-5"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Zero(t, tc.turnResult.baselineWarmPrefillTokens(time.Now(), 100_000, 0, "gpt-5.6-sol", tc.baseline, tc.historyTruncated))
		})
	}
}
