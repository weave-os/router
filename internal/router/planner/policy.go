// Package planner decides, per turn, whether to stay on a session's
// pinned model (preserving the upstream prompt cache) or switch to the
// scorer's fresh recommendation. Pure function of (pin, fresh decision,
// estimated tokens, available models); no I/O.
//
// EV math:
//
//	savings_per_turn = (pin $/M-tok × pinMult − fresh $/M-tok × freshMult) × tokens
//	eviction_cost    = fresh $/M-tok × tokens × (1 − freshMult)
//
// where pinMult/freshMult are per-model cache-read multipliers from
// catalog.Pricing.EffectiveCacheReadMultiplier. Switches when
// (expected_savings − eviction_cost) > threshold, or when tier-upgrade
// guard fires (fresh is strictly higher tier than pin).
//
// Cache-warmth gate: the cache-read multipliers and the eviction cost only
// apply while the pin's upstream prompt cache is still warm. When Inputs
// reports the pin cold (the provider's cache TTL has lapsed — short and
// best-effort on the OSS compat providers, unlike Anthropic's 1h window),
// both sides are priced uncached (pinMult = freshMult = 1, eviction_cost = 0):
// staying buys no cache reuse the fresh route wouldn't also pay, and the
// one-time prefill is incurred either way. This stops a phantom cache from
// gluing a session to a stale pin once the real cache is gone.
package planner

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

// Outcome is the planner's verdict for this turn.
type Outcome int

const (
	OutcomeStay   Outcome = iota // Keep on pinned model.
	OutcomeSwitch                // Route to fresh scorer recommendation.
)

// Decision is the planner's output. Reason is a short snake_case label.
type Decision struct {
	Outcome            Outcome
	Reason             string
	ExpectedSavingsUSD float64
	EvictionCostUSD    float64
	ThresholdUSD       float64
	// PinPriceFallback and FreshPriceFallback are true when the named provider
	// binding has no catalog entry and pricing fell back to the model's primary binding.
	PinPriceFallback   bool
	FreshPriceFallback bool
	// Shadow* is calibration-only; Outcome continues to use the production policy.
	ShadowComputed           bool
	ShadowOutcome            Outcome
	ShadowExpectedSavingsUSD float64
	ShadowStayCostUSD        float64
	ShadowSwitchCostUSD      float64
	// PinCacheCold echoes the warmth assumption the EV math ran under, for
	// observability. Only meaningful on the EV path; false on early returns.
	PinCacheCold bool
}

// EVConfig parameterizes the policy. Constructed once at boot from env.
type EVConfig struct {
	// ThresholdUSD is the minimum positive EV over the horizon to switch.
	// Default $0.001 keeps noise from flipping decisions.
	ThresholdUSD float64
	// ExpectedRemainingTurns amortizes per-turn savings into a horizon.
	// Default 3 reflects observed session lengths.
	ExpectedRemainingTurns int
	// TierUpgradeEnabled overturns an EV stay when fresh is strictly higher
	// tier than pin. Upgrade-only by design.
	TierUpgradeEnabled bool
	// ColdPinFollowFresh overturns an EV stay when the pin's cache is cold:
	// with nothing warm to preserve, the scorer's fresh pick wins. EV-positive
	// and tier-upgrade take precedence (more specific reasons). Off by
	// default; measure against shadow telemetry before arming.
	ColdPinFollowFresh bool
	// CorrectedEconomics prices the uncached tail at full rate, charges the
	// cache-write premium on a switch, and counts output price. Off by default:
	// it changes routing, so it is armed per environment. See CLAUDE.md.
	CorrectedEconomics bool
}

// Inputs is the full per-turn input to Decide.
type Inputs struct {
	Pin                  sessionpin.Pin
	Fresh                router.Decision
	EstimatedInputTokens int
	// CacheablePrefixTokens estimates the stable portion of the input.
	// Shadow-only until CorrectedEconomics is armed, which reads it as the
	// numerator of the cacheable share. Meaningful only when CachePrefixKnown.
	CacheablePrefixTokens int
	// CachePrefixKnown distinguishes a measured zero prefix from no telemetry.
	// Without it a genuinely uncached pin falls back to k=1 and is priced as
	// fully cached — the inversion CorrectedEconomics exists to remove.
	CachePrefixKnown bool
	// PriorOutputTokens estimates this turn's completion from the last one, so
	// CorrectedEconomics can price output — which the legacy path ignores
	// entirely. Zero disables the term rather than asserting a free completion.
	PriorOutputTokens int
	AvailableModels   map[string]struct{}
	// PinCacheCold reports that the pin's upstream prompt cache has lapsed —
	// no turn completed within the pinned provider's cache TTL. The proxy
	// computes this (it owns the clock); the planner stays a pure function.
	// When true, the EV math prices both sides uncached. The zero value means
	// "assume warm", preserving the original cache-discounted behavior for any
	// caller that does not supply warmth information.
	PinCacheCold bool
	// SubsidizedCostFactor scales a model's effective price in the EV math, in
	// [epsilon, 1], for models a caller's subscription covers (see
	// internal/proxy/usage). Without it, the planner prices the fresh
	// (switch-to) model at full catalog rate, so a session pinned to a cheap
	// model would never switch to a now-near-free subscription model and the
	// discount would never take effect on sticky sessions. nil = no subsidy.
	SubsidizedCostFactor map[string]float64
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
	ReasonColdPinFresh    = "cold_pin_follow_fresh"
	// ReasonSameTierPinned is set by the caller (not Decide) when a same-tier
	// lateral switch is suppressed by hmmSameTierPin.
	ReasonSameTierPinned = "same_tier_pinned"
)

// Decide returns the planner verdict for this turn.
func Decide(in Inputs, cfg EVConfig) Decision {
	if in.Pin.Model == "" {
		return Decision{Outcome: OutcomeSwitch, Reason: ReasonNoPin}
	}

	if in.Fresh.Model == in.Pin.Model {
		return Decision{Outcome: OutcomeStay, Reason: ReasonSameModel}
	}

	// Pin's model no longer routable: switch regardless of EV.
	// nil AvailableModels means "no filter" — preserve pin.
	if in.AvailableModels != nil {
		if _, ok := in.AvailableModels[in.Pin.Model]; !ok {
			return Decision{Outcome: OutcomeSwitch, Reason: ReasonPinModelMissing}
		}
	}

	// No completed turn yet: no evidence upstream cache is warm.
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
	// Subscription discount: price a covered model at its subsidized marginal
	// cost in the EV math too, so a pin on a cheap model correctly switches to a
	// now-near-free subscription model (and a covered pin is priced cheap to
	// stay). Scale Input/Output uniformly; CacheReadMultiplier is a ratio and
	// stays correct. Keyed on map MEMBERSHIP (not factor sign) to mirror the
	// scorer exactly — so a legitimate 0.0 factor (epsilon=0) discounts here too,
	// rather than being mistaken for an absent/uncovered model.
	pinPrice = applySubsidy(pinPrice, in.SubsidizedCostFactor, in.Pin.Model)
	freshPrice = applySubsidy(freshPrice, in.SubsidizedCostFactor, in.Fresh.Model)

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
	case cfg.ColdPinFollowFresh && in.PinCacheCold:
		d.Outcome = OutcomeSwitch
		d.Reason = ReasonColdPinFresh
	default:
		d.Outcome = OutcomeStay
		d.Reason = ReasonEVNegative
	}
	return d
}

// legacyEV is the original math, retained verbatim so CorrectedEconomics=false
// is bit-for-bit unchanged.
func legacyEV(pin, fresh catalog.Pricing, tokens float64, pinCold bool, cfg EVConfig) (savings, eviction float64) {
	// Cache multipliers apply only while the pin is warm; a cold pin pays full
	// rate on both sides, so eviction is zero and raw economics decide.
	pinMult, freshMult := 1.0, 1.0
	if !pinCold {
		pinMult = pin.EffectiveCacheReadMultiplier()
		freshMult = fresh.EffectiveCacheReadMultiplier()
		eviction = fresh.InputUSDPer1M * tokens * (1 - freshMult) / 1e6
	}
	perTurn := (pin.InputUSDPer1M*pinMult - fresh.InputUSDPer1M*freshMult) * tokens / 1e6
	return perTurn * float64(cfg.ExpectedRemainingTurns), eviction
}

// effectiveRate is a model's $/token on a warm turn at cacheable share k:
// price * (1 - k*(1-m)). k=1 collapses to price*m, the legacy assumption.
func effectiveRate(p catalog.Pricing, k float64) float64 {
	return p.InputUSDPer1M * (1 - k*(1-p.EffectiveCacheReadMultiplier())) / 1e6
}

// correctedEV prices both sides at their effective warm rate, counts output,
// and charges eviction as the cache write paid in place of the forgone read.
func correctedEV(pin, fresh catalog.Pricing, in Inputs, cfg EVConfig) (savings, eviction float64) {
	tokens := float64(in.EstimatedInputTokens)
	k := cacheableShare(in)
	if in.PinCacheCold {
		// Nothing live to read or destroy; the prefill is paid either way.
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

// cacheableShare is the pin's own previous-turn cache-hit share; persistence
// beat a trained model on 154k production turns. A measured zero is a real
// cold prefix and must stay 0 — only the absence of telemetry falls back to 1,
// the legacy assumption, so an uninstrumented caller degrades safely.
func cacheableShare(in Inputs) float64 {
	if !in.CachePrefixKnown {
		return 1
	}
	if in.EstimatedInputTokens <= 0 || in.CacheablePrefixTokens <= 0 {
		return 0
	}
	return min(1, float64(in.CacheablePrefixTokens)/float64(in.EstimatedInputTokens))
}

// shadowCosts evaluates the inclusive-action model in shadow only.
// N counts the current action once; K cacheable-prefix tokens get cache pricing.
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

// priceForDecision returns the price for the named provider/model binding,
// falling back to the model's primary binding when the provider is absent from the catalog.
func priceForDecision(provider, model string) (catalog.Pricing, bool, bool) {
	if provider != "" {
		if price, ok := catalog.PriceFor(provider, model); ok {
			return price, false, true
		}
	}
	price, ok := catalog.PrimaryPriceFor(model)
	return price, true, ok
}

// applySubsidy scales a model's price by its subscription cost factor when the
// model is present in factors (the covered set), leaving it unchanged otherwise.
// Keyed on membership rather than the factor's sign so a legitimate 0.0 factor
// (epsilon=0) still discounts — matching the scorer, which also keys on the map.
func applySubsidy(p catalog.Pricing, factors map[string]float64, model string) catalog.Pricing {
	f, ok := factors[model]
	if !ok {
		return p
	}
	p.InputUSDPer1M *= f
	p.OutputUSDPer1M *= f
	return p
}

// tierUpgrade reports whether fresh is strictly higher tier than pin.
// Unknown on either side disables the guard.
func tierUpgrade(pin, fresh string) bool {
	pinTier := catalog.TierFor(pin)
	freshTier := catalog.TierFor(fresh)
	if pinTier == catalog.TierUnknown || freshTier == catalog.TierUnknown {
		return false
	}
	return freshTier > pinTier
}
