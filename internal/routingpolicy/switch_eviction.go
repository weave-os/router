package routingpolicy

import (
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

const (
	evictionLookbackMessages = 20
	evictionBasePrefixTokens = 30_000
	evictionOutsideLookback  = 0.97
)

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
	base := min(float64(evictionBasePrefixTokens), prompt)
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
	if !known || turn.prefixBroken || state.ServedAt.IsZero() {
		return 1
	}
	m, ok := catalog.ByID(model)
	if !ok || turn.now.Sub(state.ServedAt) >= providers.CacheTTLFor(m.PrimaryProvider()) {
		return 1
	}
	appended := 2*turn.turnsSince - 1
	if state.MessageCount > 0 && turn.messageCount > 0 {
		appended = turn.messageCount - state.MessageCount
	}
	if appended < 0 || appended >= evictionLookbackMessages {
		return evictionOutsideLookback
	}
	row := 0
	if turn.userPrompt {
		row = 1
	}
	switch {
	case appended <= 3:
		return evictionRewriteRate[row][0]
	case appended <= 11:
		return evictionRewriteRate[row][1]
	default:
		return evictionRewriteRate[row][2]
	}
}

func expectedTurnUSD(model string, outlook CacheOutlook, tokens, outputTokens int) (float64, bool) {
	m, ok := catalog.ByID(model)
	price, priced := catalog.PrimaryPriceFor(model)
	if !ok || !priced {
		return 0, false
	}
	price = price.ForInputTokens(tokens)
	in := price.InputUSDPer1M / 1e6
	return outlook.ReadTokens*in*price.EffectiveCacheReadMultiplier() +
		outlook.WriteTokens*in*price.EffectiveCacheWrite1hMultiplier(m.PrimaryProvider()) +
		float64(outputTokens)*price.OutputUSDPer1M/1e6, true
}
