package proxy

import (
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

func (res *turnLoopResult) serveForceModelPin(forceModelPin sessionpin.Pin) {
	res.Decision = forceModelPin.Decision()
	res.Decision.Reason = translate.ReasonUserForceModel
	res.UserForced = true
	res.StickyHit = true
	res.PinTier = translate.ReasonUserForceModel
}
