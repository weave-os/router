package hmm

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/sessionpin"
)

type AuthoritativeHold = continuity.AuthoritativeHold

const (
	AuthoritativeServe                      = continuity.AuthoritativeServe
	AuthoritativeHoldUpgradeConfidenceLow   = continuity.AuthoritativeHoldUpgradeConfidenceLow
	AuthoritativeHoldDowngradeConfidenceLow = continuity.AuthoritativeHoldDowngradeConfidenceLow
	AuthoritativeHoldDowngradeHysteresis    = continuity.AuthoritativeHoldDowngradeHysteresis
)

type AuthoritativeGuards continuity.AuthoritativeGuards

type AuthoritativeVerdict = continuity.AuthoritativeVerdict

func (g AuthoritativeGuards) Judge(pin sessionpin.Pin, pinFound bool, fresh router.Decision, inputTokens int) AuthoritativeVerdict {
	verdict := AuthoritativeVerdict{Threshold: g.ConfidenceThreshold}
	if !pinFound || pin.Model == "" || pin.Model == fresh.Model {
		return verdict
	}
	confidence, scored := DecisionConfidence(fresh)
	verdict.Confidence = confidence
	unconfident := scored && confidence < g.ConfidenceThreshold
	if FreshIsMoreExpensive(pin.Model, fresh.Model, inputTokens) {
		if g.UpgradeGate && unconfident {
			verdict.Hold = AuthoritativeHoldUpgradeConfidenceLow
		}
		return verdict
	}
	verdict.Downgrade = true
	verdict.HysteresisTurns = g.HysteresisTurns
	if g.DowngradeGate && unconfident {
		verdict.Hold = AuthoritativeHoldDowngradeConfidenceLow
		verdict.DowngradeVotes = pin.ConsecutiveDowngradeVotes
		return verdict
	}
	verdict.DowngradeVotes = pin.ConsecutiveDowngradeVotes + 1
	if g.HysteresisTurns > 0 && verdict.DowngradeVotes < g.HysteresisTurns {
		verdict.Hold = AuthoritativeHoldDowngradeHysteresis
		return verdict
	}
	verdict.ShadowHysteresisTurns = g.ShadowHysteresisTurns
	verdict.HysteresisWouldHold = g.ShadowHysteresisTurns > 0 && verdict.DowngradeVotes < g.ShadowHysteresisTurns
	verdict.ConfidenceWouldHold = unconfident
	return verdict
}
