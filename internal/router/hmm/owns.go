package hmm

import (
	"strings"

	"weave-os/router/internal/router"
)

func Owns(strategy router.Strategy) bool {
	return router.IsHMMStrategy(strategy)
}

func IsDecision(dec router.Decision) bool {
	if dec.Metadata != nil && Owns(router.Strategy(dec.Metadata.Strategy)) {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(dec.Reason), "hmm_policy")
}
