package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

// The share must come from two MEASURED counters on the pin, not from measured
// cached tokens over an ESTIMATED current total — the latter biases k toward 1,
// which is the assumption the corrected economics exist to remove.
func TestCacheablePrefixUsesMeasuredRatioNotCurrentEstimate(t *testing.T) {
	pin := sessionpin.Pin{
		Provider:             providers.ProviderAnthropic,
		LastInputTokens:      10_000,
		LastCachedReadTokens: 90_000,
	}
	// Prior turn: 90k cached of 100k total = 0.9. Projected onto a 50k prompt
	// that is 45k, even though 90k cached exceeds this turn's whole estimate.
	got, known := cacheablePrefixTokens(pin, 50_000, false)
	assert.True(t, known)
	assert.Equal(t, 45_000, got, "share is scale-free; it must not clamp to the current total")
}

// input_tokens is fresh-only on Anthropic but already cache-inclusive on
// OpenAI-compatible providers. Same counters, different denominators.
func TestCacheablePrefixHonoursProviderTokenBasis(t *testing.T) {
	counters := sessionpin.Pin{LastInputTokens: 100_000, LastCachedReadTokens: 80_000}

	anthropic := counters
	anthropic.Provider = providers.ProviderAnthropic
	got, _ := cacheablePrefixTokens(anthropic, 100_000, false)
	// Disjoint: 80k / (100k + 80k) = 0.444
	assert.Equal(t, 44_444, got)

	compat := counters
	compat.Provider = providers.ProviderOpenAI
	got, _ = cacheablePrefixTokens(compat, 100_000, false)
	// Inclusive: 80k / 100k = 0.8
	assert.Equal(t, 80_000, got)
}

// A pin with no usage telemetry must report "unknown" so the planner can take
// the legacy fallback, while a client trim is a MEASURED eviction of zero.
func TestCacheablePrefixDistinguishesUnknownFromMeasuredZero(t *testing.T) {
	_, known := cacheablePrefixTokens(sessionpin.Pin{}, 50_000, false)
	assert.False(t, known, "a pin that never completed a turn has no evidence")

	trimmed, trimmedKnown := cacheablePrefixTokens(
		sessionpin.Pin{Provider: providers.ProviderAnthropic, LastInputTokens: 10_000, LastCachedReadTokens: 90_000},
		50_000, true,
	)
	assert.True(t, trimmedKnown, "a client trim is observed, not unknown")
	assert.Zero(t, trimmed)
}

// The corrected token estimate must stay behind the flag. Legacy EV scales
// linearly with token count against a fixed dollar threshold, so feeding it a
// larger number would move STAY/SWITCH the moment this deploys.
func TestPlannerTokensStayLegacyUntilFlagIsArmed(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(
		`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}],` +
			`"tools":[{"name":"t","description":"` + longDescription(4000) + `"}]}`,
	))
	require.NoError(t, err)
	feats := translate.RoutingFeatures{Tokens: 10}

	off := &Service{planner: planner.EVConfig{CorrectedEconomics: false}}
	assert.Equal(t, feats.Tokens, off.plannerTokensFor(env, feats),
		"flag off must leave the legacy estimate untouched")

	on := &Service{planner: planner.EVConfig{CorrectedEconomics: true}}
	assert.Greater(t, on.plannerTokensFor(env, feats), feats.Tokens,
		"flag on counts tool definitions the text-only estimate misses")
}

func longDescription(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'x'
	}
	return string(out)
}
