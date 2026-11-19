package hmm

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/sessionpin"
)

type SlidingThreshold struct {
	Served          bool
	Probability     float64
	FreshGroup      string
	ClassifiedGroup string
}

func SlidingThresholdServes(req router.Request, pin sessionpin.Pin, fresh router.Decision) SlidingThreshold {
	served, probability := slidingThresholdServes(req, pin, fresh)
	if !served {
		return SlidingThreshold{Probability: probability}
	}
	return SlidingThreshold{
		Served:          true,
		Probability:     probability,
		FreshGroup:      fresh.Metadata.PolicyGroup,
		ClassifiedGroup: fresh.Metadata.ClassifierPredictedLabel,
	}
}

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
