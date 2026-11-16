package main

import (
	"crypto/rand"
	"math/big"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	randomrouter "weave-os/router/internal/router/random_router"
	"weave-os/router/internal/router/rl"
	"weave-os/router/internal/routingpolicy"
)

func withRoutingPolicy(svc *proxy.Service, plans *policy.PlanResolver) *proxy.Service {
	return svc.WithRoutingPolicy(routingpolicy.New(plans))
}

func withBenchmarkPolicies(s *proxy.Service, rlRouter router.Router) *proxy.Service {
	return s.WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyRL, Router: rlRouter, Unavailable: rl.ErrPolicyUnavailable}).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyRandomBenchmark, Router: randomrouter.New(func(size int) (int, error) {
			index, err := rand.Int(rand.Reader, big.NewInt(int64(size)))
			if err != nil {
				return 0, err
			}
			return int(index.Int64()), nil
		}), Unavailable: randomrouter.ErrUnavailable})
}
