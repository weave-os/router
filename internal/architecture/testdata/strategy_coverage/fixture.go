package strategycoverage

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

// Reported: a router.Router implementation outside every strategy root.
type shadowStrategy struct{}

func (shadowStrategy) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{}, nil
}

// Reported: a StrategySpec registration whose Router is declared here.
type registeredStrategy struct{ router.Router }

var registration = policy.StrategySpec{Strategy: "fixture", Router: registeredStrategy{}}

type positionalStrategy struct{ router.Router }

// Reported: a positional StrategySpec literal registers a router too.
var positionalRegistration = policy.StrategySpec{"positional", positionalStrategy{}, nil, policy.Capabilities{}}
