package proxy

import (
	"weave-os/router/internal/router"
)

const effortHysteresisThreshold = 1.0

const effortHysteresisReason = "effort_hysteresis"

func effortHysteresisHold(fresh router.Decision, prevServedModel, chosenModel, chosenEffort string) string {
	if fresh.Metadata == nil || len(fresh.Metadata.ArmScores) == 0 {
		return ""
	}
	incumbentEffort, incumbentModel := stripEffortSuffix(prevServedModel)
	if incumbentEffort == "" || chosenEffort == "" || incumbentEffort == chosenEffort {
		return ""
	}
	if chosenModel != incumbentModel || fresh.Model != incumbentModel {
		return ""
	}
	_, armBase := stripEffortSuffix(fresh.Metadata.SelectedRosterArmID)
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

func appendEffortHysteresisReason(reason string) string {
	if reason == "" {
		return effortHysteresisReason
	}
	return reason + "+" + effortHysteresisReason
}
