package routingpolicy

import (
	"context"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/continuity"
)

const switchAuthoritativePinTierPrefix = "decide_switch_"

func (t SessionTurn) serveSwitchVerdict(ctx context.Context, res *TurnRoute, shadow switchShadowTurn) {
	if !flags.BoolOr(ctx, flags.KeySwitchDecideAuthoritative, false) {
		return
	}
	verdict := DecideSwitch(shadow.switchInput(*res))
	live := res.Decision.Model
	if verdict.Serve == "" || verdict.Serve == live {
		return
	}
	switch verdict.Serve {
	case shadow.Gate.Fresh.Model:
		res.Decision = shadow.Gate.Fresh
		res.StickyHit, res.StickyRole = false, ""
	case shadow.StayPin.Model:
		res.Decision = shadow.StayPin.Decision()
		res.StickyHit, res.StickyRole = true, t.Ports.History.HistoryRole(res.PinRole)
	default:
		return
	}
	res.PinTier = switchAuthoritativePinTierPrefix + string(verdict.Reason)
	logSwitchAuthoritative(ctx, live, res.Decision.Model, verdict, shadow.Served.UpgradeConfidence)
}

func (t SessionTurn) freshPickSwitchOutcome(ctx context.Context, res TurnRoute) (continuity.Outcome, bool) {
	if !t.PinFound || !flags.BoolOr(ctx, flags.KeySwitchDecideAuthoritative, false) {
		return continuity.Outcome{}, false
	}
	shadow := switchShadowTurn{
		StayPin: t.Pin,
		Gate: continuity.CostGateInput{
			Request: t.Request, ActivePin: t.Pin, HMMHistory: t.SwitchHistory, Fresh: t.Fresh,
			EstimatedInputTokens: t.PlannerTokens, PrefixBroken: t.PrefixBroken,
		},
		Served: continuity.CostGateServe{UpgradeConfidence: 1},
		Now:    t.Now(),
	}
	verdict := DecideSwitch(shadow.switchInput(res))
	tier := switchAuthoritativePinTierPrefix + string(verdict.Reason)
	switch verdict.Serve {
	case t.Pin.Model:
		logSwitchAuthoritative(ctx, t.Fresh.Model, t.Pin.Model, verdict, 1)
		return continuity.Outcome{Stay: true, PinTier: tier}, true
	case t.Fresh.Model:
		handover := continuity.HandoverSummarize
		if t.PrefixBroken {
			handover = continuity.HandoverSkippedTrimmedHistory
		}
		logSwitchAuthoritative(ctx, t.Fresh.Model, t.Fresh.Model, verdict, 1)
		return continuity.Outcome{PinTier: tier, Handover: handover}, true
	}
	return continuity.Outcome{}, false
}

func logSwitchAuthoritative(ctx context.Context, proposed, served string, verdict SwitchVerdict, upgradeConfidence float64) {
	observability.FromContext(ctx).Info("router.switch_authoritative", "proposed_model", proposed, "served_model", served, "reason", string(verdict.Reason), "eviction_usd", verdict.Economics.EvictionUSD, "move_value_usd", verdict.Economics.MoveValueUSD, "downgrade", verdict.Economics.Downgrade, "upgrade_confidence", upgradeConfidence)
}
