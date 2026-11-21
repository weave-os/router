package routingpolicy

const (
	switchCompactMinEvictionUSD      = 0.25
	switchCompactSummaryOutputTokens = 4_000
)

func compactionPays(input SwitchInput, tokens, output int, eviction, moveValue float64) bool {
	compacted := input.CompactedTokens
	if compacted <= 0 || 2*compacted > tokens || eviction <= input.Tuning.compactMinEvictionUSD() {
		return false
	}
	summary, _ := expectedTurnUSD(
		input.Current,
		predictCacheOutlook(input.Current, switchCurrentCache(input), true, switchTurn(input, tokens, 1)),
		tokens,
		switchCompactSummaryOutputTokens,
	)
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
