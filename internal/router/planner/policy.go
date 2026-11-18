package planner

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

type Outcome int

const (
	OutcomeStay Outcome = iota
	OutcomeSwitch
)

type Decision struct {
	Outcome                  Outcome
	Reason                   string
	ExpectedSavingsUSD       float64
	EvictionCostUSD          float64
	ThresholdUSD             float64
	PinPriceFallback         bool
	FreshPriceFallback       bool
	ShadowComputed           bool
	ShadowOutcome            Outcome
	ShadowExpectedSavingsUSD float64
	ShadowStayCostUSD        float64
	ShadowSwitchCostUSD      float64
	PinCacheCold             bool
}

type EVConfig struct {
	ThresholdUSD           float64
	ExpectedRemainingTurns int
	TierUpgradeEnabled     bool
	CorrectedEconomics     bool
}

type Inputs struct {
	Pin                   sessionpin.Pin
	Fresh                 router.Decision
	EstimatedInputTokens  int
	CacheablePrefixTokens int
	CachePrefixKnown      bool
	PriorOutputTokens     int
	AvailableModels       map[string]struct{}
	PinCacheCold          bool
}

const (
	ReasonNoPin           = "no_pin"
	ReasonSameModel       = "same_model"
	ReasonPinModelMissing = "pin_model_missing"
	ReasonNoPriorUsage    = "no_prior_usage"
	ReasonPricingMissing  = "pricing_missing"
	ReasonEVPositive      = "ev_positive"
	ReasonEVNegative      = "ev_negative"
	ReasonTierUpgrade     = "tier_upgrade"
)

func Decide(in Inputs, cfg EVConfig) Decision {
	if in.Pin.Model == "" {
		return Decision{Outcome: OutcomeSwitch, Reason: ReasonNoPin}
	}

	if in.Fresh.Model == in.Pin.Model {
		return Decision{Outcome: OutcomeStay, Reason: ReasonSameModel}
	}

	if in.AvailableModels != nil {
		if _, ok := in.AvailableModels[in.Pin.Model]; !ok {
			return Decision{Outcome: OutcomeSwitch, Reason: ReasonPinModelMissing}
		}
	}

	if in.Pin.LastTurnEndedAt.IsZero() {
		return Decision{Outcome: OutcomeStay, Reason: ReasonNoPriorUsage}
	}

	pinPrice, pinPriceFallback, ok1 := priceForDecision(in.Pin.Provider, in.Pin.Model)
	freshPrice, freshPriceFallback, ok2 := priceForDecision(in.Fresh.Provider, in.Fresh.Model)
	if !ok1 || !ok2 {
		return Decision{
			Outcome:            OutcomeStay,
			Reason:             ReasonPricingMissing,
			PinPriceFallback:   pinPriceFallback && ok1,
			FreshPriceFallback: freshPriceFallback && ok2,
		}
	}
	pinPrice = pinPrice.ForInputTokens(in.EstimatedInputTokens)
	freshPrice = freshPrice.ForInputTokens(in.EstimatedInputTokens)

	tokens := float64(in.EstimatedInputTokens)
	var expectedSavings, evictionCost float64
	if cfg.CorrectedEconomics {
		expectedSavings, evictionCost = correctedEV(pinPrice, freshPrice, in, cfg)
	} else {
		expectedSavings, evictionCost = legacyEV(pinPrice, freshPrice, tokens, in.PinCacheCold, cfg)
	}

	d := Decision{
		ExpectedSavingsUSD: expectedSavings,
		EvictionCostUSD:    evictionCost,
		ThresholdUSD:       cfg.ThresholdUSD,
		PinCacheCold:       in.PinCacheCold,
		PinPriceFallback:   pinPriceFallback,
		FreshPriceFallback: freshPriceFallback,
	}
	d.ShadowOutcome, d.ShadowExpectedSavingsUSD, d.ShadowStayCostUSD, d.ShadowSwitchCostUSD = shadowCosts(
		pinPrice, freshPrice, tokens, float64(in.CacheablePrefixTokens), cfg.ExpectedRemainingTurns, in.PinCacheCold, cfg.ThresholdUSD,
	)
	d.ShadowComputed = true
	switch {
	case expectedSavings-evictionCost > cfg.ThresholdUSD:
		d.Outcome = OutcomeSwitch
		d.Reason = ReasonEVPositive
	case cfg.TierUpgradeEnabled && tierUpgrade(in.Pin.Model, in.Fresh.Model):
		d.Outcome = OutcomeSwitch
		d.Reason = ReasonTierUpgrade
	default:
		d.Outcome = OutcomeStay
		d.Reason = ReasonEVNegative
	}
	return d
}

func legacyEV(pin, fresh catalog.Pricing, tokens float64, pinCold bool, cfg EVConfig) (savings, eviction float64) {
	pinMult, freshMult := 1.0, 1.0
	if !pinCold {
		pinMult = pin.EffectiveCacheReadMultiplier()
		freshMult = fresh.EffectiveCacheReadMultiplier()
		eviction = fresh.InputUSDPer1M * tokens * (1 - freshMult) / 1e6
	}
	perTurn := (pin.InputUSDPer1M*pinMult - fresh.InputUSDPer1M*freshMult) * tokens / 1e6
	return perTurn * float64(cfg.ExpectedRemainingTurns), eviction
}

func effectiveRate(p catalog.Pricing, k float64) float64 {
	return p.InputUSDPer1M * (1 - k*(1-p.EffectiveCacheReadMultiplier())) / 1e6
}

func correctedEV(pin, fresh catalog.Pricing, in Inputs, cfg EVConfig) (savings, eviction float64) {
	tokens := float64(in.EstimatedInputTokens)
	k := cacheableShare(in)
	if in.PinCacheCold {
		k = 0
	}
	perTurn := tokens*(effectiveRate(pin, k)-effectiveRate(fresh, k)) +
		float64(in.PriorOutputTokens)*(pin.OutputUSDPer1M-fresh.OutputUSDPer1M)/1e6
	if !in.PinCacheCold {
		eviction = tokens * k * fresh.InputUSDPer1M *
			(fresh.EffectiveCacheWriteMultiplier() - fresh.EffectiveCacheReadMultiplier()) / 1e6
	}
	return perTurn * float64(cfg.ExpectedRemainingTurns), eviction
}

func cacheableShare(in Inputs) float64 {
	if !in.CachePrefixKnown {
		return 1
	}
	if in.EstimatedInputTokens <= 0 || in.CacheablePrefixTokens <= 0 {
		return 0
	}
	return min(1, float64(in.CacheablePrefixTokens)/float64(in.EstimatedInputTokens))
}

func shadowCosts(pin, fresh catalog.Pricing, total, prefix float64, remaining int, pinCold bool, threshold float64) (Outcome, float64, float64, float64) {
	if remaining < 1 {
		remaining = 1
	}
	prefix = min(max(prefix, 0), total)
	tail := total - prefix
	warm := func(p catalog.Pricing) float64 {
		return (prefix*p.InputUSDPer1M*p.EffectiveCacheReadMultiplier() + tail*p.InputUSDPer1M) / 1e6
	}
	cold := func(p catalog.Pricing) float64 {
		return (prefix*p.InputUSDPer1M*p.EffectiveCacheWriteMultiplier() + tail*p.InputUSDPer1M) / 1e6
	}
	pinWarm, freshWarm := warm(pin), warm(fresh)
	stayCurrent := pinWarm
	if pinCold {
		stayCurrent = cold(pin)
	}
	stay := stayCurrent + float64(remaining-1)*pinWarm
	switchCost := cold(fresh) + float64(remaining-1)*freshWarm
	savings := stay - switchCost
	if savings > threshold {
		return OutcomeSwitch, savings, stay, switchCost
	}
	return OutcomeStay, savings, stay, switchCost
}

func priceForDecision(provider, model string) (catalog.Pricing, bool, bool) {
	if provider != "" {
		if price, ok := catalog.PriceFor(provider, model); ok {
			return price, false, true
		}
	}
	price, ok := catalog.PrimaryPriceFor(model)
	return price, true, ok
}

func tierUpgrade(pin, fresh string) bool {
	pinTier := catalog.TierFor(pin)
	freshTier := catalog.TierFor(fresh)
	if pinTier == catalog.TierUnknown || freshTier == catalog.TierUnknown {
		return false
	}
	return freshTier > pinTier
}
