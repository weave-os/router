package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/sessionpin"
)

func endedAgo(d time.Duration) time.Time {
	return time.Now().Add(-d)
}

func TestBaselineWarmPrefillTokens_SwitchWithinBaselineTTLIsCorrected(t *testing.T) {
	turnResult := turnLoopResult{PriorServedModel: "claude-opus-5", PriorServedEndedAt: endedAgo(2 * time.Minute)}

	assert.Equal(t, 100_000, turnResult.baselineWarmPrefillTokens(100_000, "gpt-5.6-sol", "claude-opus-5", false),
		"switching away from the baseline pays a cold prefill the baseline would have read warm")
}

func TestBaselineWarmPrefillTokens_SwitchBackToBaselineIsCorrected(t *testing.T) {
	turnResult := turnLoopResult{PriorServedModel: "gpt-5.6-sol", PriorServedEndedAt: endedAgo(2 * time.Minute)}

	assert.Equal(t, 100_000, turnResult.baselineWarmPrefillTokens(100_000, "claude-opus-5", "claude-opus-5", false),
		"returning to the baseline re-primes a cache the baseline never let go cold")
}

// An effort change keeps the base model, and a baseline that never switched
// carries the client's effort anyway, so it is not a router-caused prefill.
func TestBaselineWarmPrefillTokens_EffortOnlyChangeIsNotASwitch(t *testing.T) {
	turnResult := turnLoopResult{PriorServedModel: "gpt-5.6-sol:low", PriorServedEndedAt: endedAgo(time.Minute)}

	assert.Zero(t, turnResult.baselineWarmPrefillTokens(100_000, "gpt-5.6-sol", "claude-opus-5", false))
}

// The TTL that matters is the baseline's: Anthropic holds the prefix for an
// hour, OpenAI for five minutes.
func TestBaselineWarmPrefillTokens_UsesBaselineProviderTTL(t *testing.T) {
	turnResult := turnLoopResult{PriorServedModel: "gpt-5.6-sol", PriorServedEndedAt: endedAgo(20 * time.Minute)}

	assert.Equal(t, 100_000, turnResult.baselineWarmPrefillTokens(100_000, "claude-opus-5", "claude-opus-5", false),
		"a 20m gap is inside the Anthropic baseline's cache TTL")

	turnResult = turnLoopResult{PriorServedModel: "claude-opus-5", PriorServedEndedAt: endedAgo(20 * time.Minute)}
	assert.Zero(t, turnResult.baselineWarmPrefillTokens(100_000, "gpt-5.6-sol", "gpt-5.6-sol", false),
		"a 20m gap expires the OpenAI baseline's cache, so it would have re-primed too")
}

// A fresh HMM switch has no thread pin; the prior served turn lives only on
// the HMM history pin, and its end time must come from that same pin.
func TestApplySwitchHistory_TimesThePriorTurnFromTheSelectedPin(t *testing.T) {
	hmmHistory := sessionpin.Pin{LastServedModel: "claude-opus-5", LastTurnEndedAt: endedAgo(2 * time.Minute)}
	var turnResult turnLoopResult
	turnResult.applySwitchHistory(sessionpin.Pin{}, hmmHistory)

	assert.Equal(t, 100_000, turnResult.baselineWarmPrefillTokens(100_000, "gpt-5.6-sol", "claude-opus-5", false))
}

func TestBaselineWarmPrefillTokens_BaselineWouldAlsoBeCold(t *testing.T) {
	cases := map[string]struct {
		turnResult       turnLoopResult
		baseline         string
		historyTruncated bool
	}{
		"first turn":         {turnResult: turnLoopResult{PriorServedEndedAt: endedAgo(time.Minute)}, baseline: "claude-opus-5"},
		"no prior turn time": {turnResult: turnLoopResult{PriorServedModel: "claude-opus-5"}, baseline: "claude-opus-5"},
		"client trimmed":     {turnResult: turnLoopResult{PriorServedModel: "claude-opus-5", PriorServedEndedAt: endedAgo(time.Minute), PrefixTrimmed: true}, baseline: "claude-opus-5"},
		"ingress truncation": {turnResult: turnLoopResult{PriorServedModel: "claude-opus-5", PriorServedEndedAt: endedAgo(time.Minute)}, baseline: "claude-opus-5", historyTruncated: true},
		"unpriced baseline":  {turnResult: turnLoopResult{PriorServedModel: "claude-opus-5", PriorServedEndedAt: endedAgo(time.Minute)}, baseline: "not-a-model"},
		"cache gap past TTL": {turnResult: turnLoopResult{PriorServedModel: "claude-opus-5", PriorServedEndedAt: endedAgo(2 * time.Hour)}, baseline: "claude-opus-5"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Zero(t, tc.turnResult.baselineWarmPrefillTokens(100_000, "gpt-5.6-sol", tc.baseline, tc.historyTruncated))
		})
	}
}
