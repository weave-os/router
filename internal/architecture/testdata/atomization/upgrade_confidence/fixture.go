package upgradeconfidence

import (
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/hmm"
)

type gate struct {
	// Counted: a threshold field declared outside internal/router/hmm.
	UpgradeConfidenceThreshold float64
}

// Counted: a threshold constant declared outside internal/router/hmm.
const localUpgradeConfidenceThreshold = 0.7

func allow(confidence float64) bool {
	// Counted: the field key, the continuity default, the field read and the local constant.
	g := gate{UpgradeConfidenceThreshold: continuity.DefaultUpgradeConfidenceThreshold}
	// Not counted: a threshold declared in internal/router/hmm.
	if confidence >= hmm.DefaultUpgradeConfidenceThreshold {
		return true
	}
	return confidence >= g.UpgradeConfidenceThreshold || confidence >= localUpgradeConfidenceThreshold
}
