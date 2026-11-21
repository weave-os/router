package catalog

import "weave-os/router/internal/router/escalation"

func EscalationTier(group escalation.Group) Tier {
	switch group {
	case escalation.Low:
		return TierLow
	case escalation.Medium:
		return TierMid
	case escalation.High, escalation.Maximum:
		return TierHigh
	}
	return TierUnknown
}
