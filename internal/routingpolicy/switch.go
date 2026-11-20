package routingpolicy

import (
	"math"
	"time"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

type SwitchReason string

const (
	SwitchReasonFirstTurn          SwitchReason = "first_turn"
	SwitchReasonSameModel          SwitchReason = "same_model"
	SwitchReasonCurrentUnavailable SwitchReason = "current_unavailable"
	SwitchReasonPricingMissing     SwitchReason = "pricing_missing"
	SwitchReasonDowngrade          SwitchReason = "downgrade"
	SwitchReasonUpgrade            SwitchReason = "upgrade"
	SwitchReasonUpgradePending     SwitchReason = "upgrade_rewrite_exceeds_value"
	SwitchReasonSideRequest        SwitchReason = "side_request"
	SwitchReasonDowngradePending   SwitchReason = "downgrade_rewrite_exceeds_savings"
	SwitchReasonCompactThenSwitch  SwitchReason = "compact_then_switch"
)

const (
	switchDowngradeHorizonTurns = 10
	switchUpgradeHorizonTurns   = 40
	switchDefaultOutputTokens   = 1_500
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
	Now                   time.Time
	MessageCount          int
	UserPrompt            bool
	ModelCaches           sessionpin.ModelCaches
	CompactedTokens       int
}

type SwitchVerdict struct {
	Serve   string
	Switch  bool
	Compact bool
	Reason  SwitchReason
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
	output := input.OutputTokens
	if output <= 0 {
		output = switchDefaultOutputTokens
	}
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
	currentTurn, _ := expectedTurnUSD(input.Current, predictCacheOutlook(input.Current, switchCurrentCache(input), true, switchTurn(input, tokens, 1)), tokens, output)
	freshState, freshKnown := switchFreshCache(input)
	freshTurn, _ := expectedTurnUSD(input.Fresh, predictCacheOutlook(input.Fresh, freshState, freshKnown, switchTurn(input, tokens, input.FreshTurnsSinceServed)), tokens, output)
	eviction := (freshTurn - freshWarm) - (currentTurn - currentWarm)
	downgrade := freshWarm < currentWarm
	horizon := float64(switchUpgradeHorizonTurns)
	if downgrade {
		horizon = switchDowngradeHorizonTurns
	}
	moveValue := horizon * math.Abs(currentWarm-freshWarm)
	if compactionPays(input, tokens, output, eviction, moveValue) {
		return SwitchVerdict{Serve: input.Fresh, Switch: true, Compact: true, Reason: SwitchReasonCompactThenSwitch}
	}
	pays := moveValue > eviction
	switch {
	case downgrade && pays:
		return move(SwitchReasonDowngrade)
	case downgrade:
		return stay(SwitchReasonDowngradePending)
	case pays:
		return move(SwitchReasonUpgrade)
	default:
		return stay(SwitchReasonUpgradePending)
	}
}

func switchPricingTokens(input SwitchInput) int {
	return max(input.PromptTokens, input.EstimatedInputTokens)
}

func switchTurn(input SwitchInput, tokens, turnsSince int) evictionTurn {
	return evictionTurn{
		now:          switchNow(input),
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
	return sessionpin.ModelCacheState{ServedAt: switchNow(input), PromptTokens: input.PromptTokens}
}

func switchFreshCache(input SwitchInput) (sessionpin.ModelCacheState, bool) {
	if state, ok := input.ModelCaches[input.Fresh]; ok {
		return state, true
	}
	if input.FreshTurnsSinceServed > 0 {
		return sessionpin.ModelCacheState{ServedAt: switchNow(input), PromptTokens: input.PromptTokens}, true
	}
	return sessionpin.ModelCacheState{}, false
}

func switchNow(input SwitchInput) time.Time {
	if input.Now.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return input.Now
}

func switchWarmTurnUSD(model string, tokens, outputTokens int) (float64, bool) {
	if _, ok := catalog.ByID(model); !ok {
		return 0, false
	}
	return expectedTurnUSD(model, CacheOutlook{ReadTokens: float64(tokens)}, tokens, outputTokens)
}
