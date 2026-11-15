package routingpolicy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

// AuthoritativeHold names the guard that kept the session pin over an
// authoritative per-turn pick. AuthoritativeServe means no guard held it.
type AuthoritativeHold int

const (
	AuthoritativeServe AuthoritativeHold = iota
	AuthoritativeHoldUpgradeConfidenceLow
	AuthoritativeHoldDowngradeConfidenceLow
	AuthoritativeHoldDowngradeHysteresis
)

// AuthoritativeGuards are the gates an authoritative per-turn pick passes
// before it displaces a live session pin. Authoritative selection bypasses the
// HMM cost gate, but the confidence floor still applies to pricier picks, and
// cheaper picks can be held by a downgrade floor and vote hysteresis.
type AuthoritativeGuards struct {
	ConfidenceThreshold float64
	// UpgradeGate applies the confidence floor to picks pricier than the pin.
	UpgradeGate bool
	// DowngradeGate applies the confidence floor to picks cheaper than the pin.
	DowngradeGate bool
	// HysteresisTurns is the consecutive cheaper votes a downgrade needs; 0 disables it.
	HysteresisTurns int
	// ShadowHysteresisTurns only scores a served downgrade; it never holds one.
	ShadowHysteresisTurns int
}

// AuthoritativeVerdict is what the guards decided about one pick.
type AuthoritativeVerdict struct {
	Hold       AuthoritativeHold
	Threshold  float64
	Confidence float64
	// Downgrade is true when the pick is cheaper than the pin; the fields below
	// are only set for downgrades.
	Downgrade bool
	// DowngradeVotes is the cheaper-vote count to persist on a hold, or the
	// count a served downgrade arrived with, including itself.
	DowngradeVotes        int
	HysteresisTurns       int
	ShadowHysteresisTurns int
	HysteresisWouldHold   bool
	ConfidenceWouldHold   bool
}

// Judge decides whether pin should be kept over the authoritative pick fresh.
// Unpinned turns and same-model picks always serve, as do unscored picks
// unless hysteresis holds them. An unconfident cheaper vote is discarded
// before hysteresis counts it, so noise cannot accumulate into a switch.
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
