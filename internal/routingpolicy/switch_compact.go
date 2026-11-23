package routingpolicy

const (
	switchCompactMinEvictionUSD      = 0.25
	switchCompactSummaryOutputTokens = 4_000
)

func compactionPays(input SwitchInput, tokens, output int, eviction, moveValue float64) bool {
	compacted := input.CompactedTokens
	if compacted <= 0 || 2*compacted > tokens || tokens < input.Tuning.CompactMinPromptTokens ||
		eviction <= input.Tuning.compactMinEvictionUSD() {
		return false
	}
	summary := compactionSummaryUSD(input, tokens)
	base := float64(min(evictionBasePrefixTokens, compacted))
	firstTurn, _ := expectedTurnUSD(
		input.Fresh,
		CacheOutlook{RewriteProbability: 1, ReadTokens: base, WriteTokens: float64(compacted) - base},
		compacted,
		output,
	)
	warmTurn, _ := switchWarmTurnUSD(input.Fresh, compacted, output)
	oneTime := summary + firstTurn - warmTurn
	return oneTime < eviction && oneTime < moveValue
}

func compactionSummaryUSD(input SwitchInput, tokens int) float64 {
	if input.SummaryReady {
		return 0
	}
	model, state, known := input.Current, switchCurrentCache(input), true
	if input.Summarizer != "" && input.Summarizer != input.Current {
		model = input.Summarizer
		state, known = input.ModelCaches[model]
	}
	usd, _ := expectedTurnUSD(model, predictCacheOutlook(model, state, known, switchTurn(input, tokens, 1)), tokens, switchCompactSummaryOutputTokens)
	return usd
}
