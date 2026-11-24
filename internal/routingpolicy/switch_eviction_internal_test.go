package routingpolicy

import (
	"testing"
	"time"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRewriteProbabilityFollowsTheDestinationProvidersCache(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	turn := evictionTurn{now: now, promptTokens: 200_000, messageCount: 330, turnsSince: 15}
	cases := []struct {
		name  string
		model string
		state sessionpin.ModelCacheState
		want  float64
	}{
		{"block-lookback cache misses after 20+ appended messages", "claude-sonnet-5-5", sessionpin.ModelCacheState{ServedAt: now.Add(-2 * time.Minute), PromptTokens: 190_000, MessageCount: 300}, evictionOutsideLookback},
		{"prefix cache has no block lookback", "gpt-6.1-sol", sessionpin.ModelCacheState{ServedAt: now.Add(-2 * time.Minute), PromptTokens: 190_000, MessageCount: 300}, evictionPrefixCacheRewriteRate[0]},
		{"prefix cache mostly survives a ten minute gap", "gpt-6.1-sol", sessionpin.ModelCacheState{ServedAt: now.Add(-10 * time.Minute), PromptTokens: 190_000, MessageCount: 300}, evictionPrefixCacheRewriteRate[1]},
		{"prefix cache after an hour", "gpt-6.1-sol", sessionpin.ModelCacheState{ServedAt: now.Add(-2 * time.Hour), PromptTokens: 190_000, MessageCount: 300}, evictionPrefixCacheRewriteRate[2]},
		{"prefix cache rewrites after the thread shrank", "gpt-6.1-sol", sessionpin.ModelCacheState{ServedAt: now.Add(-time.Minute), PromptTokens: 190_000, MessageCount: 400}, evictionOutsideLookback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.want, rewriteProbability(tc.model, tc.state, true, turn), 1e-9)
		})
	}
}

func TestRewriteProbabilityUsesTheRecordedWriteTTL(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	turn := evictionTurn{now: now, promptTokens: 200_000, messageCount: 304, turnsSince: 2}
	fiveMinute := sessionpin.ModelCacheState{ServedAt: now.Add(-10 * time.Minute), PromptTokens: 190_000, MessageCount: 300, WriteTTLSeconds: 300}
	oneHour := fiveMinute
	oneHour.WriteTTLSeconds = 3600
	assert.InDelta(t, 1.0, rewriteProbability("claude-sonnet-5-5", fiveMinute, true, turn), 1e-9)
	assert.InDelta(t, evictionRewriteRate[0][1], rewriteProbability("claude-sonnet-5-5", oneHour, true, turn), 1e-9)
}

func TestUnseenDestinationReadsOnlyTheSharedPrefix(t *testing.T) {
	turn := evictionTurn{now: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC), promptTokens: 200_000}
	for _, model := range []string{"gpt-6.1-sol", "claude-sonnet-5-5"} {
		outlook := predictCacheOutlook(model, sessionpin.ModelCacheState{}, false, turn)
		assert.InDelta(t, 12_000, outlook.ReadTokens, 1e-9, model)
		assert.InDelta(t, 188_000, outlook.WriteTokens, 1e-9, model)
	}
}

// Prefix-cached providers bill an uncached prompt at the plain input rate:
// there is no cache-write premium to predict, whatever the catalog binding
// carries for billing.
func TestPrefixCachedRewriteIsPricedAsUncachedInput(t *testing.T) {
	const tokens = 100_000
	outlook := CacheOutlook{WriteTokens: tokens}
	for _, model := range []string{"gpt-6.1-sol", "gpt-6-luna", "gemini-3.8-flash"} {
		price, ok := catalog.PrimaryPriceFor(model)
		require.True(t, ok, model)
		assert.Greater(t, price.EffectiveCacheWriteMultiplier(), 1.0, "catalog write multiplier is unchanged for %s", model)
		got, priced := expectedTurnUSD(model, outlook, tokens, 0, 0)
		require.True(t, priced, model)
		assert.InDelta(t, tokens*price.ForInputTokens(tokens).InputUSDPer1M/1e6, got, 1e-9, model)
	}
	price, _ := catalog.PrimaryPriceFor("claude-sonnet-5-5")
	got, _ := expectedTurnUSD("claude-sonnet-5-5", outlook, tokens, 0, 5*time.Minute)
	assert.InDelta(t, tokens*price.InputUSDPer1M/1e6*price.EffectiveCacheWriteMultiplier(), got, 1e-9)
}

// A router prefix trim is stable across turns, so a prefix cache keeps
// matching the trimmed prompt it already saw; only block caches lose the
// breakpoint they were written at.
func TestPrefixTrimRewritesOnlyBlockCaches(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	turn := evictionTurn{now: now, promptTokens: 200_000, messageCount: 304, turnsSince: 2, prefixBroken: true}
	state := sessionpin.ModelCacheState{ServedAt: now.Add(-2 * time.Minute), PromptTokens: 190_000, MessageCount: 300}
	assert.InDelta(t, evictionPrefixCacheRewriteRate[0], rewriteProbability("gpt-6.1-sol", state, true, turn), 1e-9)
	assert.InDelta(t, 1.0, rewriteProbability("claude-sonnet-5-5", state, true, turn), 1e-9)
}
