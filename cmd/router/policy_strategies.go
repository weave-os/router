package main

import (
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/rl"
)

func withRLPolicyStrategy(service *proxy.Service, rlRouter router.Router) *proxy.Service {
	return service.WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyRL, Router: rlRouter, Unavailable: rl.ErrPolicyUnavailable})
}
