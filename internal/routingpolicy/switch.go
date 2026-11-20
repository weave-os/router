package routingpolicy

import "weave-os/router/internal/router/catalog"

type SwitchReason string

const (
	SwitchReasonFirstTurn          SwitchReason = "first_turn"
	SwitchReasonSameModel          SwitchReason = "same_model"
	SwitchReasonCurrentUnavailable SwitchReason = "current_unavailable"
	SwitchReasonPricingMissing     SwitchReason = "pricing_missing"
	SwitchReasonDowngrade          SwitchReason = "downgrade"
	SwitchReasonUpgradeFree        SwitchReason = "upgrade_free"
	SwitchReasonUpgradeVotes       SwitchReason = "upgrade_votes"
	SwitchReasonUpgradePending     SwitchReason = "upgrade_pending"
	SwitchReasonSideRequest        SwitchReason = "side_request"
	SwitchReasonDowngradeWarm      SwitchReason = "downgrade_warm_return"
	SwitchReasonDowngradePending   SwitchReason = "downgrade_rewrite_exceeds_savings"
)

const (
	switchUpgradeWindow   = 3
	switchUpgradeVotes    = 2
	switchWarmReturnTurns = 2
	switchPaybackTurns    = 10
)

type SwitchInput struct {
	Current               string
	Fresh                 string
	PriorPicks            []string
	EstimatedInputTokens  int
	PromptTokens          int
	FreshTurnsSinceServed int
	CacheCold             bool
	PrefixBroken          bool
	SideRequest           bool
}

type SwitchVerdict struct {
	Serve  string
	Switch bool
	Reason SwitchReason
}

func DecideSwitch(input SwitchInput) SwitchVerdict {
	stay := func(reason SwitchReason) SwitchVerdict {
		return SwitchVerdict{Serve: input.Current, Reason: reason}
	}
	move := func(reason SwitchReason) SwitchVerdict {
		return SwitchVerdict{Serve: input.Fresh, Switch: true, Reason: reason}
	}
	if input.Current == "" {
		return move(SwitchReasonFirstTurn)
	}
	if input.Fresh == input.Current {
		return stay(SwitchReasonSameModel)
	}
	if input.SideRequest {
		return stay(SwitchReasonSideRequest)
	}
	tokens := switchPricingTokens(input)
	currentPrice, currentPriced := HMMEffectiveInputUSDPer1M(input.Current, tokens)
	if !currentPriced {
		return move(SwitchReasonCurrentUnavailable)
	}
	freshPrice, freshPriced := HMMEffectiveInputUSDPer1M(input.Fresh, tokens)
	rewriteFree := input.CacheCold || input.PrefixBroken || switchFreshCacheWarm(input)
	switch {
	case !freshPriced:
		return stay(SwitchReasonPricingMissing)
	case freshPrice == currentPrice:
		return stay(SwitchReasonSameModel)
	case freshPrice < currentPrice && input.CacheCold, freshPrice < currentPrice && input.PrefixBroken:
		return move(SwitchReasonDowngrade)
	case freshPrice < currentPrice && rewriteFree:
		return move(SwitchReasonDowngradeWarm)
	case freshPrice < currentPrice && switchDowngradePaysBack(input.Current, input.Fresh, tokens):
		return move(SwitchReasonDowngrade)
	case freshPrice < currentPrice:
		return stay(SwitchReasonDowngradePending)
	case rewriteFree:
		return move(SwitchReasonUpgradeFree)
	case switchUpgradeVoteCount(input, currentPrice, tokens) >= switchUpgradeVotes:
		return move(SwitchReasonUpgradeVotes)
	default:
		return stay(SwitchReasonUpgradePending)
	}
}

func switchPricingTokens(input SwitchInput) int {
	return max(input.PromptTokens, input.EstimatedInputTokens)
}

func switchFreshCacheWarm(input SwitchInput) bool {
	return input.FreshTurnsSinceServed > 0 && input.FreshTurnsSinceServed <= switchWarmReturnTurns
}

func switchDowngradePaysBack(current, fresh string, tokens int) bool {
	currentPrice, currentOK := catalog.PrimaryPriceFor(current)
	freshPrice, freshOK := catalog.PrimaryPriceFor(fresh)
	if !currentOK || !freshOK {
		return true
	}
	currentPrice, freshPrice = currentPrice.ForInputTokens(tokens), freshPrice.ForInputTokens(tokens)
	readSavingsPerTurn := currentPrice.InputUSDPer1M*currentPrice.CacheReadMultiplier - freshPrice.InputUSDPer1M*freshPrice.CacheReadMultiplier
	rewrite := freshPrice.InputUSDPer1M * freshPrice.EffectiveCacheWriteMultiplier()
	return readSavingsPerTurn*switchPaybackTurns > rewrite
}

func switchUpgradeVoteCount(input SwitchInput, currentPrice float64, tokens int) int {
	votes := 1
	prior := input.PriorPicks
	if len(prior) > switchUpgradeWindow-1 {
		prior = prior[len(prior)-(switchUpgradeWindow-1):]
	}
	for _, pick := range prior {
		price, priced := HMMEffectiveInputUSDPer1M(pick, tokens)
		if priced && price > currentPrice {
			votes++
		}
	}
	return votes
}
