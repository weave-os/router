package routingpolicy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/sessionpin"
)

func ExcludedRerouteServable(rerouted router.Decision, req router.Request, availableModels map[string]struct{}) bool {
	if availableModels != nil {
		if _, available := availableModels[rerouted.Model]; !available {
			return false
		}
	}
	return candidates.PinEligible(sessionpin.Pin{Model: rerouted.Model, Provider: rerouted.Provider}, req)
}
