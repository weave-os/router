package hmm

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/routingpolicy"
)

const effortHysteresisThreshold = 1.0

const EffortHysteresisReason = "effort_hysteresis"

func EffortHysteresisHold(fresh router.Decision, prevServedModel, chosenModel, chosenEffort string) string {
	if fresh.Metadata == nil || len(fresh.Metadata.ArmScores) == 0 {
		return ""
	}
	incumbentEffort, incumbentModel := routingpolicy.StripEffortSuffix(prevServedModel)
	if incumbentEffort == "" || chosenEffort == "" || incumbentEffort == chosenEffort {
		return ""
	}
	if chosenModel != incumbentModel || fresh.Model != incumbentModel {
		return ""
	}
	_, armBase := routingpolicy.StripEffortSuffix(fresh.Metadata.SelectedRosterArmID)
	if armBase == "" {
		return ""
	}
	challengerScore, ok := fresh.Metadata.ArmScores[armBase+":"+chosenEffort]
	if !ok {
		return ""
	}
	incumbentScore, ok := fresh.Metadata.ArmScores[armBase+":"+incumbentEffort]
	if !ok {
		return ""
	}
	if challengerScore-incumbentScore >= effortHysteresisThreshold {
		return ""
	}
	return incumbentEffort
}

func AppendEffortHysteresisReason(reason string) string {
	if reason == "" {
		return EffortHysteresisReason
	}
	return reason + "+" + EffortHysteresisReason
}
