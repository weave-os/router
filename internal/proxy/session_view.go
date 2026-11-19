package proxy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

func sessionPinView(pin sessionpin.Pin, found bool) router.SessionPin {
	return router.SessionPin{
		Found:            found,
		Provider:         pin.Provider,
		Model:            pin.Model,
		LastTurnEndedAt:  pin.LastTurnEndedAt,
		LastOutputTokens: pin.LastOutputTokens,
	}
}
