package routingpolicy

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
)

const (
	switchUpgradeWindow = 3
	switchUpgradeVotes  = 2
)

type SwitchInput struct {
	Current              string
	Fresh                string
	PriorPicks           []string
	EstimatedInputTokens int
	CacheCold            bool
	PrefixBroken         bool
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
	currentPrice, currentPriced := HMMEffectiveInputUSDPer1M(input.Current, input.EstimatedInputTokens)
	if !currentPriced {
		return move(SwitchReasonCurrentUnavailable)
	}
	freshPrice, freshPriced := HMMEffectiveInputUSDPer1M(input.Fresh, input.EstimatedInputTokens)
	switch {
	case !freshPriced:
		return stay(SwitchReasonPricingMissing)
	case freshPrice == currentPrice:
		return stay(SwitchReasonSameModel)
	case freshPrice < currentPrice:
		return move(SwitchReasonDowngrade)
	case input.CacheCold || input.PrefixBroken:
		return move(SwitchReasonUpgradeFree)
	case switchUpgradeVoteCount(input, currentPrice) >= switchUpgradeVotes:
		return move(SwitchReasonUpgradeVotes)
	default:
		return stay(SwitchReasonUpgradePending)
	}
}

func switchUpgradeVoteCount(input SwitchInput, currentPrice float64) int {
	votes := 1
	prior := input.PriorPicks
	if len(prior) > switchUpgradeWindow-1 {
		prior = prior[len(prior)-(switchUpgradeWindow-1):]
	}
	for _, pick := range prior {
		price, priced := HMMEffectiveInputUSDPer1M(pick, input.EstimatedInputTokens)
		if priced && price > currentPrice {
			votes++
		}
	}
	return votes
}
