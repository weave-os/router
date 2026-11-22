package routingpolicy

import (
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/sessionstrategy"
)

type SessionStores struct {
	Pins       sessionpin.Store
	Strategies sessionstrategy.Store
}
