package hmm

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/sessionpin"
)

type SlidingThreshold = continuity.SlidingThreshold

func SlidingThresholdServes(req router.Request, pin sessionpin.Pin, fresh router.Decision) SlidingThreshold {
	if req.Session.SlidingThresholdPinGroup == "" || pin.Model == "" || pin.Model == fresh.Model || fresh.Metadata == nil {
		return SlidingThreshold{}
	}
	label := fresh.Metadata.ClassifierPredictedLabel
	probability, scored := fresh.Metadata.ClassifierProbabilities[label]
	if !scored {
		return SlidingThreshold{}
	}
	target, covered := escalation.SlidingThresholdTarget(req.Session.SlidingThresholdPinGroup, escalation.Group(label), probability)
	if !covered || target == "" || escalation.Group(fresh.Metadata.PolicyGroup) != target {
		return SlidingThreshold{Probability: probability}
	}
	return SlidingThreshold{Served: true, Probability: probability, FreshGroup: fresh.Metadata.PolicyGroup, ClassifiedGroup: label}
}
