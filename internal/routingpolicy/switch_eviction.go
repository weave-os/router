package routingpolicy

import (
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

const (
	evictionLookbackMessages   = 20
	evictionBasePrefixTokens   = 30_000
	evictionSharedPrefixTokens = 12_000
	evictionOutsideLookback    = 0.97

	evictionPrefixCacheShortGap = 5 * time.Minute
	evictionPrefixCacheLongGap  = time.Hour
)

var evictionPrefixCacheRewriteRate = [3]float64{0.05, 0.20, 0.45}

var evictionRewriteRate = [2][3]float64{
	{0.005, 0.07, 0.11},
	{0.12, 0.19, 0.31},
}

type CacheOutlook struct {
	RewriteProbability float64
	ReadTokens         float64
	WriteTokens        float64
}

type evictionTurn struct {
	now          time.Time
	promptTokens int
	messageCount int
	turnsSince   int
	userPrompt   bool
	prefixBroken bool
}

func predictCacheOutlook(model string, state sessionpin.ModelCacheState, known bool, turn evictionTurn) CacheOutlook {
	p := rewriteProbability(model, state, known, turn)
	prompt := float64(turn.promptTokens)
	base := min(float64(evictionSharedPrefixTokens), prompt)
	if known {
		base = min(float64(evictionBasePrefixTokens), prompt)
	}
	prior := 0.0
	if known {
		prior = min(float64(state.PromptTokens), prompt)
	}
	return CacheOutlook{
		RewriteProbability: p,
		ReadTokens:         p*base + (1-p)*prior,
		WriteTokens:        p*(prompt-base) + (1-p)*(prompt-prior),
	}
}

func rewriteProbability(model string, state sessionpin.ModelCacheState, known bool, turn evictionTurn) float64 {
	if !known || state.ServedAt.IsZero() {
		return 1
	}
	m, ok := catalog.ByID(model)
	if !ok {
		return 1
	}
	if turn.prefixBroken && providers.FamilyFor(m.PrimaryProvider()) == providers.FamilyAnthropic {
		return 1
	}
	appended := 2*turn.turnsSince - 1
	if state.MessageCount > 0 && turn.messageCount > 0 {
		appended = turn.messageCount - state.MessageCount
	}
	if providers.FamilyFor(m.PrimaryProvider()) != providers.FamilyAnthropic {
		return prefixCacheRewriteProbability(turn.now.Sub(state.ServedAt), appended)
	}
	if turn.now.Sub(state.ServedAt) >= recordedCacheTTL(m, state) {
		return 1
	}
	if appended < 0 || appended >= evictionLookbackMessages {
		return evictionOutsideLookback
	}
	userPromptRow := 0
	if turn.userPrompt {
		userPromptRow = 1
	}
	switch {
	case appended <= 3:
		return evictionRewriteRate[userPromptRow][0]
	case appended <= 11:
		return evictionRewriteRate[userPromptRow][1]
	default:
		return evictionRewriteRate[userPromptRow][2]
	}
}

func recordedCacheTTL(m catalog.Model, state sessionpin.ModelCacheState) time.Duration {
	if state.WriteTTLSeconds > 0 {
		return time.Duration(state.WriteTTLSeconds) * time.Second
	}
	return providers.CacheTTLFor(m.PrimaryProvider())
}

func prefixCacheRewriteProbability(gap time.Duration, appended int) float64 {
	switch {
	case appended < 0:
		return evictionOutsideLookback
	case gap < evictionPrefixCacheShortGap:
		return evictionPrefixCacheRewriteRate[0]
	case gap < evictionPrefixCacheLongGap:
		return evictionPrefixCacheRewriteRate[1]
	default:
		return evictionPrefixCacheRewriteRate[2]
	}
}

func rewriteInputMultiplier(m catalog.Model, price catalog.Pricing, writeTTL time.Duration) float64 {
	if providers.FamilyFor(m.PrimaryProvider()) != providers.FamilyAnthropic {
		return 1
	}
	if writeTTL >= time.Hour {
		return price.EffectiveCacheWrite1hMultiplier(m.PrimaryProvider())
	}
	return price.EffectiveCacheWriteMultiplier()
}

func expectedTurnUSD(model string, outlook CacheOutlook, tokens, outputTokens int, writeTTL time.Duration) (float64, bool) {
	m, ok := catalog.ByID(model)
	price, priced := catalog.PrimaryPriceFor(model)
	if !ok || !priced {
		return 0, false
	}
	price = price.ForInputTokens(tokens)
	in := price.InputUSDPer1M / 1e6
	if writeTTL <= 0 {
		writeTTL = providers.CacheTTLFor(m.PrimaryProvider())
	}
	writeMultiplier := rewriteInputMultiplier(m, price, writeTTL)
	return outlook.ReadTokens*in*price.EffectiveCacheReadMultiplier() +
		outlook.WriteTokens*in*writeMultiplier +
		float64(outputTokens)*price.OutputUSDPer1M/1e6, true
}
