package routingpolicy

import (
	"context"
	"slices"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

func (t SessionTurn) UpgradeDecision(pin sessionpin.Pin, res TurnRoute) *AuthoritativeUpgradeDecision {
	fresh, req, inputTokens := t.Fresh, t.Request, t.PlannerTokens
	if !t.Ports.Ownership.Owns(res.Strategy) || !t.Ports.Ownership.IsDecision(fresh) {
		return nil
	}
	mode := t.Settings.UpgradePolicy
	if mode == flags.AuthoritativeUpgradePolicyScore && !t.Settings.UpgradeGate {
		mode = flags.AuthoritativeUpgradePolicyOff
	}
	upgradeShadow := t.Settings.Config.UpgradeShadow
	decision := &AuthoritativeUpgradeDecision{
		Mode:            mode,
		PinModel:        pin.Model,
		PinProvider:     pin.Provider,
		PinTier:         catalog.TierFor(pin.Model),
		RequestedTier:   res.RequestedTier,
		MarginThreshold: upgradeShadow.MarginThreshold,
	}
	decision.Evidence = continuity.UpgradeEvidence{
		HasPin:         pin.Model != "",
		SameModel:      pin.Model == fresh.Model,
		FreshEligible:  candidates.AutomaticPinEligible(sessionpin.Pin{Model: fresh.Model, Provider: fresh.Provider}, req),
		Demoted:        slices.Contains(res.SessionDemotedModels, fresh.Model),
		FreshGroup:     escalation.Group(DecisionPolicyGroup(fresh)),
		PinGroup:       escalation.Group(t.Ports.Facts.PinStateOf(pin).PolicyGroup),
		RequestedGroup: escalation.Group(req.ForceCluster),
		PrefixBroken:   res.PrefixTrimmed || req.HistoryTruncated,
		RequiredVotes:  t.Settings.UpgradeVotes,
	}
	if t.Settings.AvailableModels != nil {
		_, available := t.Settings.AvailableModels[fresh.Model]
		decision.Evidence.FreshEligible = decision.Evidence.FreshEligible && available
	}
	if fresh.Metadata != nil {
		decision.Evidence.Margin = t.Ports.Facts.ClassifierMargin(fresh)
		score := float64(fresh.Metadata.ChosenScore)
		decision.FreshScore = &score
	}
	if decision.Evidence.Margin != nil && decision.MarginThreshold != nil {
		qualified := *decision.Evidence.Margin >= *decision.MarginThreshold
		decision.Evidence.MarginQualified = &qualified
	}
	if decision.Evidence.HasPin {
		decision.Evidence.CacheCold = PinCacheCold(pin, decision.Evidence.PrefixBroken, t.Now())
		_, pinPriced := HMMEffectiveInputUSDPer1M(pin.Model, inputTokens)
		_, freshPriced := HMMEffectiveInputUSDPer1M(fresh.Model, inputTokens)
		decision.Evidence.PricingKnown = pinPriced && freshPriced
		decision.Evidence.MoreExpensive = t.Ports.Upgrade.FreshIsMoreExpensive(pin.Model, fresh.Model, inputTokens)
		if !pin.FirstPinnedAt.IsZero() {
			age := PinAge(pin, t.Now())
			decision.PinAgeSec = &age
			decision.Evidence.StalePin = upgradeShadow.StalePinAfter > 0 && t.Now().Sub(pin.FirstPinnedAt) >= upgradeShadow.StalePinAfter
		}
	}
	if upgradeShadow.StalePinAfter > 0 {
		seconds := int64(upgradeShadow.StalePinAfter.Seconds())
		decision.StalePinAfterSec = &seconds
	}
	votes := 0
	if decision.Evidence.HasPin && decision.Evidence.MoreExpensive && !decision.Evidence.SameModel &&
		decision.Evidence.FreshEligible && !decision.Evidence.Demoted &&
		decision.Evidence.FreshGroup == decision.Evidence.PinGroup &&
		!decision.Evidence.PrefixBroken && !decision.Evidence.CacheCold && !decision.Evidence.StalePin {
		votes = t.Ports.Facts.PinStateOf(pin).UpgradeVotes + 1
	}
	decision.Evidence.VoteCount = &votes
	decision.Verdict = t.Ports.Upgrade.EvaluateAuthoritativeUpgrade(decision.Evidence)
	return decision
}

func (t SessionTurn) cacheShadow(activePin sessionpin.Pin) AuthorityCacheShadow {
	fresh := t.Fresh
	if !t.Settings.CacheShadow || !t.Ports.Ownership.IsDecision(fresh) {
		return AuthorityCacheShadow{}
	}
	_, plannerDecision, sticky, stayModel := CostGatedDecision(t.Ports, t.Settings.CostGate, continuity.CostGateInput{
		Request: t.Request, ActivePin: activePin, HMMHistory: t.SwitchHistory, Fresh: fresh,
		EstimatedInputTokens: t.PlannerTokens, PrefixBroken: t.PrefixBroken, Planner: t.Settings.Planner, Now: t.Now(),
	})
	stayProvider := activePin.Provider
	stayIdentity := stayModel
	stayPin, stayPinOK := t.Ports.CostGate.StayPin(t.Settings.CostGate, t.Request, activePin, t.SwitchHistory, t.Now())
	if stayPinOK && stayPin.Model == stayModel {
		stayProvider = stayPin.Provider
		stayIdentity = stayPin.Decision().ServedIdentity()
	}
	freshRosterArmID := ""
	if fresh.Metadata != nil {
		freshRosterArmID = fresh.Metadata.SelectedRosterArmID
	}
	shadow := AuthorityCacheShadow{
		Computed:   true,
		Decision:   plannerDecision,
		StayModel:  stayModel,
		Sticky:     sticky,
		StayScore:  t.Ports.Facts.CandidateScore(fresh, stayIdentity, stayProvider, ""),
		FreshScore: t.Ports.Facts.CandidateScore(fresh, fresh.ServedIdentity(), fresh.Provider, freshRosterArmID),
	}
	if stayPinOK && stayPin.Model == stayModel {
		shadow.StayProvider = stayPin.Provider
	}
	return shadow
}

func CostGatedDecision(ports continuity.StrategyPorts, gate continuity.CostGateConfig, input continuity.CostGateInput) (router.Decision, planner.Decision, bool, string) {
	stayPin, plan, sticky := ports.CostGate.Decide(gate, input)
	if sticky {
		return stayPin.Decision(), plan, true, stayPin.Model
	}
	return input.Fresh, plan, false, stayPin.Model
}

func logAuthorityCacheShadow(ctx context.Context, res TurnRoute) {
	if !res.AuthorityShadow.Computed {
		return
	}
	shadow := res.AuthorityShadow
	log := observability.FromContext(ctx)
	log.Info("authoritative turn cache-gate shadow",
		"turn_type", string(res.TurnType),
		"fresh_model", res.Fresh.Model,
		"shadow_outcome", PlannerOutcome(shadow.Decision.Outcome),
		"shadow_reason", shadow.Decision.Reason,
		"shadow_stay_model", shadow.StayModel,
		"shadow_would_diverge", shadow.Sticky,
		"shadow_ev_ran", shadow.EVRan(),
	)
	if !shadow.EVRan() {
		return
	}
	log.Info("authoritative turn cache-gate shadow EV",
		"shadow_expected_savings_usd", shadow.Decision.ExpectedSavingsUSD,
		"shadow_eviction_cost_usd", shadow.Decision.EvictionCostUSD,
		"shadow_pin_cache_cold", shadow.Decision.PinCacheCold,
		"shadow_corrected_outcome", PlannerOutcome(shadow.Decision.ShadowOutcome),
		"shadow_corrected_savings_usd", shadow.Decision.ShadowExpectedSavingsUSD,
	)
}

func PlannerOutcome(outcome planner.Outcome) string {
	if outcome == planner.OutcomeSwitch {
		return "switch"
	}
	return "stay"
}

func PinAge(p sessionpin.Pin, now time.Time) int64 {
	if p.FirstPinnedAt.IsZero() {
		return 0
	}
	d := now.Sub(p.FirstPinnedAt)
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}
