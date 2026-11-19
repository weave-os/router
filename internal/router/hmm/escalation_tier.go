package hmm

import (
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
)

func EscalationTier(group escalation.Group) catalog.Tier {
	switch group {
	case escalation.Low:
		return catalog.TierLow
	case escalation.Medium:
		return catalog.TierMid
	case escalation.High, escalation.Maximum:
		return catalog.TierHigh
	}
	return catalog.TierUnknown
}
