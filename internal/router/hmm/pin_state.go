package hmm

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

type PinState struct {
	PolicyGroup    string
	DowngradeVotes int
	UpgradeVotes   int
}

func PinStateOf(pin sessionpin.Pin) PinState {
	return PinState{
		PolicyGroup:    pin.PolicyGroup,
		DowngradeVotes: pin.ConsecutiveDowngradeVotes,
		UpgradeVotes:   pin.ConsecutiveUpgradeVotes,
	}
}

func ClassifierMargin(dec router.Decision) *float64 {
	if dec.Metadata == nil {
		return nil
	}
	return dec.Metadata.ClassifierMargin
}
