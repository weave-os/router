package candidates

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
)

func HardPinReason(turnType turntype.TurnType) string {
	return string(turnType) + "_hard_pin"
}

func HardPinDecision(turnType turntype.TurnType, provider, model string) router.Decision {
	return router.Decision{Provider: provider, Model: model, Reason: HardPinReason(turnType)}
}
