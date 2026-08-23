package proxy

import (
	"strings"

	"workweave/router/internal/router"
)

// effortHysteresisThreshold is the minimum WMI-score gap required for an
// effort-only change on the same base model within the same cluster.
// Below this, the incumbent is held to avoid cache-prefix invalidation
// and thinking-block thrash from noise.
const effortHysteresisThreshold = 1.0

// applyEffortHysteresis decides whether to keep the incumbent effort or switch
// to the challenger. Both are same-cluster arms the sidecar ranked within the
// same complexity bucket, so their scores (wmi_low/wmi_medium/wmi_high/wmi_max)
// are comparable.
//
// Returns true when hysteresis passed (switch is allowed or N/A).
func applyEffortHysteresis(prev router.Decision, fresh router.Decision) bool {
	if fresh.Metadata == nil || fresh.Metadata.ArmScores == nil {
		return true
	}
	if fresh.Metadata.PolicyGroup == "" || prev.Metadata == nil || prev.Metadata.PolicyGroup == "" {
		return true
	}
	if fresh.Metadata.PolicyGroup != prev.Metadata.PolicyGroup {
		return true
	}
	incumbentID := prev.ServedIdentity()
	if incumbentID == fresh.ServedIdentity() {
		return true
	}
	incumbentBase, _ := splitModelEffort(incumbentID)
	challengerBase, _ := splitModelEffort(fresh.ServedIdentity())
	if incumbentBase == "" || incumbentBase != challengerBase {
		return true
	}
	incumbentScore := fresh.Metadata.ArmScores[prev.ServedIdentity()]
	challengerScore := fresh.Metadata.ArmScores[fresh.ServedIdentity()]
	if incumbentScore == 0 && challengerScore == 0 {
		return true
	}
	return challengerScore-incumbentScore >= effortHysteresisThreshold
}

func splitModelEffort(servedIdentity string) (string, string) {
	if idx := strings.LastIndex(servedIdentity, ":"); idx > 0 {
		return servedIdentity[:idx], servedIdentity[idx+1:]
	}
	return servedIdentity, ""
}
