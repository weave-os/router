package pinstate

import "weave-os/router/internal/router/sessionpin"

type Accessor struct{}

var _ sessionpin.StrategyColumnAccessor = Accessor{}

func (Accessor) Columns(pin sessionpin.Pin) sessionpin.StrategyColumns {
	return Columns(pin)
}

func (Accessor) WithColumns(pin sessionpin.Pin, columns sessionpin.StrategyColumns) sessionpin.Pin {
	return WithColumns(pin, columns)
}

func Columns(pin sessionpin.Pin) sessionpin.StrategyColumns {
	return sessionpin.StrategyColumns{
		PolicyGroup:               pin.PolicyGroup,
		ConsecutiveDowngradeVotes: pin.ConsecutiveDowngradeVotes,
		ConsecutiveUpgradeVotes:   pin.ConsecutiveUpgradeVotes,
	}
}

func WithColumns(pin sessionpin.Pin, columns sessionpin.StrategyColumns) sessionpin.Pin {
	pin.PolicyGroup = columns.PolicyGroup
	pin.ConsecutiveDowngradeVotes = columns.ConsecutiveDowngradeVotes
	pin.ConsecutiveUpgradeVotes = columns.ConsecutiveUpgradeVotes
	return pin
}
