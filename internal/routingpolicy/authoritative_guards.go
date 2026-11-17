package routingpolicy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

type AuthoritativeHold int

const (
	AuthoritativeServe AuthoritativeHold = iota
	AuthoritativeHoldUpgradeConfidenceLow
	AuthoritativeHoldDowngradeConfidenceLow
	AuthoritativeHoldDowngradeHysteresis
)

type AuthoritativeGuards struct {
	ConfidenceThreshold   float64
	UpgradeGate           bool
	DowngradeGate         bool
	HysteresisTurns       int
	ShadowHysteresisTurns int
}

type AuthoritativeVerdict struct {
	Hold                  AuthoritativeHold
	Threshold             float64
	Confidence            float64
	Downgrade             bool
	DowngradeVotes        int
	HysteresisTurns       int
	ShadowHysteresisTurns int
	HysteresisWouldHold   bool
	ConfidenceWouldHold   bool
}

func (g AuthoritativeGuards) Judge(pin sessionpin.Pin, pinFound bool, fresh router.Decision, inputTokens int) AuthoritativeVerdict {
	verdict := AuthoritativeVerdict{Threshold: g.ConfidenceThreshold}
	if !pinFound || pin.Model == "" || pin.Model == fresh.Model {
		return verdict
	}
	confidence, scored := HMMDecisionConfidence(fresh)
	verdict.Confidence = confidence
	unconfident := scored && confidence < g.ConfidenceThreshold
	if HMMFreshIsMoreExpensive(pin.Model, fresh.Model, inputTokens) {
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
