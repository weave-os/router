package pinfields

import "weave-os/router/internal/router/sessionpin"

func carry(previous sessionpin.Pin) sessionpin.Pin {
	next := sessionpin.Pin{Model: previous.Model, PolicyGroup: previous.PolicyGroup}
	next.ConsecutiveDowngradeVotes = previous.ConsecutiveUpgradeVotes
	return next
}
