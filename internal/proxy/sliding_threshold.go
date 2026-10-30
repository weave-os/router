package proxy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/sessionpin"
)

// slidingThresholdServes reports whether the sliding threshold moves the session
// to the fresh decision's group. When it does not, the existing upgrade gates
// decide the turn unchanged.
func slidingThresholdServes(req router.Request, pin sessionpin.Pin, fresh router.Decision) (served bool, probability float64) {
	if req.SlidingThresholdPinGroup == "" || pin.Model == "" || pin.Model == fresh.Model || fresh.Metadata == nil {
		return false, 0
	}
	label := fresh.Metadata.ClassifierPredictedLabel
	probability, scored := fresh.Metadata.ClassifierProbabilities[label]
	if !scored {
		return false, 0
	}
	target, covered := escalation.SlidingThresholdTarget(req.SlidingThresholdPinGroup, escalation.Group(label), probability)
	if !covered || target == "" {
		return false, probability
	}
	return escalation.Group(fresh.Metadata.PolicyGroup) == target, probability
}
