package proxy

import (
	"strings"

	"workweave/router/internal/router"
)

// effortHysteresisThreshold is the minimum WMI-score gap to hold the incumbent
// effort, avoiding cache-prefix invalidation and thinking-block thrash.
const effortHysteresisThreshold = 1.0

// applyEffortHysteresis returns true when the effort switch is allowed (or N/A).
// Same-cluster arm scores are on a shared scale, so a gap below the threshold holds the incumbent.
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
