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
	switchUpgradeWindow       = 3
	switchUpgradeVotes        = 2
	switchWarmReturnTurns     = 2
	switchPaybackTurns        = 40
	switchDefaultOutputTokens = 1_500
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
	OutputTokens          int
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
		return SwitchVerdict{Serve: input.Fresh, Reason: SwitchReasonSideRequest}
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
	case freshPrice < currentPrice && switchDowngradePaysBack(input, tokens):
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

func switchDowngradePaysBack(input SwitchInput, tokens int) bool {
	freshModel, freshOK := catalog.ByID(input.Fresh)
	currentTurn, currentOK := switchTurnCostUSDPer1M(input.Current, tokens, input.OutputTokens)
	freshTurn, freshPriced := switchTurnCostUSDPer1M(input.Fresh, tokens, input.OutputTokens)
	if !freshOK || !currentOK || !freshPriced {
		return true
	}
	freshPrice, _ := catalog.PrimaryPriceFor(input.Fresh)
	freshPrice = freshPrice.ForInputTokens(tokens)
	rewrite := float64(tokens) * freshPrice.InputUSDPer1M * freshPrice.EffectiveCacheWrite1hMultiplier(freshModel.PrimaryProvider())
	return (currentTurn-freshTurn)*switchPaybackTurns > rewrite
}

func switchTurnCostUSDPer1M(model string, tokens, outputTokens int) (float64, bool) {
	price, ok := catalog.PrimaryPriceFor(model)
	if !ok {
		return 0, false
	}
	if outputTokens <= 0 {
		outputTokens = switchDefaultOutputTokens
	}
	price = price.ForInputTokens(tokens)
	return float64(tokens)*price.InputUSDPer1M*price.EffectiveCacheReadMultiplier() + float64(outputTokens)*price.OutputUSDPer1M, true
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
