package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func gapMS(d time.Duration) *int64 {
	ms := d.Milliseconds()
	return &ms
}

func TestBaselineWarmPrefillTokens_SwitchWithinBaselineTTLIsCorrected(t *testing.T) {
	res := turnLoopResult{PriorServedModel: "claude-opus-5", PriorTurnGapMS: gapMS(2 * time.Minute)}

	assert.Equal(t, 100_000, res.baselineWarmPrefillTokens(100_000, "gpt-5.6-sol", "claude-opus-5", false),
		"switching away from the baseline pays a cold prefill the baseline would have read warm")
}

func TestBaselineWarmPrefillTokens_SwitchBackToBaselineIsCorrected(t *testing.T) {
	res := turnLoopResult{PriorServedModel: "gpt-5.6-sol", PriorTurnGapMS: gapMS(2 * time.Minute)}

	assert.Equal(t, 100_000, res.baselineWarmPrefillTokens(100_000, "claude-opus-5", "claude-opus-5", false),
		"returning to the baseline re-primes a cache the baseline never let go cold")
}

// An effort change keeps the base model, and a baseline that never switched
// carries the client's effort anyway, so it is not a router-caused prefill.
func TestBaselineWarmPrefillTokens_EffortOnlyChangeIsNotASwitch(t *testing.T) {
	res := turnLoopResult{PriorServedModel: "gpt-5.6-sol:low", PriorTurnGapMS: gapMS(time.Minute)}

	assert.Zero(t, res.baselineWarmPrefillTokens(100_000, "gpt-5.6-sol", "claude-opus-5", false))
}

// The TTL that matters is the baseline's: Anthropic holds the prefix for an
// hour, OpenAI for five minutes.
func TestBaselineWarmPrefillTokens_UsesBaselineProviderTTL(t *testing.T) {
	res := turnLoopResult{PriorServedModel: "gpt-5.6-sol", PriorTurnGapMS: gapMS(20 * time.Minute)}

	assert.Equal(t, 100_000, res.baselineWarmPrefillTokens(100_000, "claude-opus-5", "claude-opus-5", false),
		"a 20m gap is inside the Anthropic baseline's cache TTL")

	res = turnLoopResult{PriorServedModel: "claude-opus-5", PriorTurnGapMS: gapMS(20 * time.Minute)}
	assert.Zero(t, res.baselineWarmPrefillTokens(100_000, "gpt-5.6-sol", "gpt-5.6-sol", false),
		"a 20m gap expires the OpenAI baseline's cache, so it would have re-primed too")
}

func TestBaselineWarmPrefillTokens_BaselineWouldAlsoBeCold(t *testing.T) {
	cases := map[string]struct {
		res              turnLoopResult
		baseline         string
		historyTruncated bool
	}{
		"first turn":         {res: turnLoopResult{PriorTurnGapMS: gapMS(time.Minute)}, baseline: "claude-opus-5"},
		"no prior turn time": {res: turnLoopResult{PriorServedModel: "claude-opus-5"}, baseline: "claude-opus-5"},
		"client trimmed":     {res: turnLoopResult{PriorServedModel: "claude-opus-5", PriorTurnGapMS: gapMS(time.Minute), PrefixTrimmed: true}, baseline: "claude-opus-5"},
		"ingress truncation": {res: turnLoopResult{PriorServedModel: "claude-opus-5", PriorTurnGapMS: gapMS(time.Minute)}, baseline: "claude-opus-5", historyTruncated: true},
		"unpriced baseline":  {res: turnLoopResult{PriorServedModel: "claude-opus-5", PriorTurnGapMS: gapMS(time.Minute)}, baseline: "not-a-model"},
		"cache gap past TTL": {res: turnLoopResult{PriorServedModel: "claude-opus-5", PriorTurnGapMS: gapMS(2 * time.Hour)}, baseline: "claude-opus-5"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Zero(t, tc.res.baselineWarmPrefillTokens(100_000, "gpt-5.6-sol", tc.baseline, tc.historyTruncated))
		})
	}
}
