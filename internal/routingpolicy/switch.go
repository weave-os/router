package routingpolicy

import (
	"math"
	"time"

	"weave-os/router/internal/router/sessionpin"
)

type SwitchReason string

const (
	SwitchReasonFirstTurn            SwitchReason = "first_turn"
	SwitchReasonSameModel            SwitchReason = "same_model"
	SwitchReasonCurrentUnavailable   SwitchReason = "current_unavailable"
	SwitchReasonPricingMissing       SwitchReason = "pricing_missing"
	SwitchReasonDowngrade            SwitchReason = "downgrade"
	SwitchReasonUpgrade              SwitchReason = "upgrade"
	SwitchReasonUpgradePending       SwitchReason = "upgrade_rewrite_exceeds_value"
	SwitchReasonSideRequest          SwitchReason = "side_request"
	SwitchReasonDowngradePending     SwitchReason = "downgrade_rewrite_exceeds_savings"
	SwitchReasonUpgradeConfidenceLow SwitchReason = "upgrade_confidence_low"
)

const (
	switchDowngradeHorizonTurns = 10
	switchUpgradeHorizonTurns   = 25
	switchDefaultOutputTokens   = 1_500
)

type SwitchInput struct {
	Current              string
	Fresh                string
	PriorPicks           []string
	EstimatedInputTokens int
	PromptTokens         int
	CacheCold            bool
	PrefixBroken         bool
	SideRequest          bool
	Now                  time.Time
	MessageCount         int
	UserPrompt           bool
	CacheWriteTTL        time.Duration
	ModelCaches          sessionpin.ModelCaches
	UpgradeConfidence    float64
	UpgradeConfidenceLow bool
}

type SwitchVerdict struct {
	Serve     string
	Switch    bool
	Reason    SwitchReason
	Economics SwitchEconomics
}

type SwitchEconomics struct {
	Priced       bool
	EvictionUSD  float64
	MoveValueUSD float64
	HorizonTurns int
	Downgrade    bool
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
	input = withoutSiblingThreadCaches(input)
	tokens := switchPricingTokens(input)
	output := switchDefaultOutputTokens
	currentWarm, currentPriced := switchWarmTurnUSD(input.Current, tokens, output)
	if !currentPriced {
		return move(SwitchReasonCurrentUnavailable)
	}
	freshWarm, freshPriced := switchWarmTurnUSD(input.Fresh, tokens, output)
	if !freshPriced {
		return stay(SwitchReasonPricingMissing)
	}
	if freshWarm == currentWarm {
		return stay(SwitchReasonSameModel)
	}
	currentTurn, _ := expectedTurnUSD(input.Current, predictCacheOutlook(input.Current, switchCurrentCache(input), true, switchTurn(input, tokens, 1)), tokens, output, input.CacheWriteTTL)
	freshState, freshKnown := input.ModelCaches[input.Fresh]
	freshTurn, _ := expectedTurnUSD(input.Fresh, predictCacheOutlook(input.Fresh, freshState, freshKnown, switchTurn(input, tokens, 0)), tokens, output, input.CacheWriteTTL)
	eviction := (freshTurn - freshWarm) - (currentTurn - currentWarm)
	downgrade := freshWarm < currentWarm
	horizon := switchUpgradeHorizonTurns
	if downgrade {
		horizon = switchDowngradeHorizonTurns
	}
	economics := SwitchEconomics{
		Priced:       true,
		EvictionUSD:  eviction,
		MoveValueUSD: float64(horizon) * math.Abs(currentWarm-freshWarm),
		HorizonTurns: horizon,
		Downgrade:    downgrade,
	}
	verdict := stay(SwitchReasonUpgradePending)
	pays := economics.MoveValueUSD > eviction || input.CacheCold || (input.PrefixBroken && trimLosesCache(input.Current))
	switch {
	case !downgrade && input.UpgradeConfidenceLow:
		verdict = stay(SwitchReasonUpgradeConfidenceLow)
	case downgrade && pays:
		verdict = move(SwitchReasonDowngrade)
	case downgrade:
		verdict = stay(SwitchReasonDowngradePending)
	case pays:
		verdict = move(SwitchReasonUpgrade)
	}
	verdict.Economics = economics
	return verdict
}

func withoutSiblingThreadCaches(input SwitchInput) SwitchInput {
	if len(input.ModelCaches) == 0 {
		return input
	}
	caches := make(sessionpin.ModelCaches, len(input.ModelCaches))
	for model, state := range input.ModelCaches {
		if siblingThreadCache(state, input) {
			if model == input.Current {
				input.PromptTokens = 0
			}
			continue
		}
		caches[model] = state
	}
	input.ModelCaches = caches
	if len(caches) == 0 {
		input.ModelCaches = nil
	}
	return input
}

func siblingThreadCache(state sessionpin.ModelCacheState, input SwitchInput) bool {
	return !input.PrefixBroken && state.MessageCount > 0 && input.MessageCount > 0 && state.MessageCount > input.MessageCount
}

func switchPricingTokens(input SwitchInput) int {
	if input.PrefixBroken {
		return input.EstimatedInputTokens
	}
	return max(input.PromptTokens, input.EstimatedInputTokens)
}

func switchTurn(input SwitchInput, tokens, turnsSince int) evictionTurn {
	return evictionTurn{
		now:          input.Now,
		promptTokens: tokens,
		messageCount: input.MessageCount,
		turnsSince:   turnsSince,
		userPrompt:   input.UserPrompt,
		prefixBroken: input.PrefixBroken,
	}
}

func switchCurrentCache(input SwitchInput) sessionpin.ModelCacheState {
	if state, ok := input.ModelCaches[input.Current]; ok && !input.CacheCold {
		return state
	}
	if input.CacheCold {
		return sessionpin.ModelCacheState{}
	}
	prior := input.PromptTokens
	if prior <= 0 {
		prior = switchPricingTokens(input)
	}
	return sessionpin.ModelCacheState{ServedAt: input.Now, PromptTokens: prior}
}

func switchWarmTurnUSD(model string, tokens, outputTokens int) (float64, bool) {
	return expectedTurnUSD(model, CacheOutlook{ReadTokens: float64(tokens)}, tokens, outputTokens, 0)
}
