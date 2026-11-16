package main

import (
	"crypto/rand"
	"math/big"
	"weave-os/router/internal/proxy"
	randomrouter "weave-os/router/internal/random_router"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func randomBenchmarkDraw(size int) (int, error) {
	index, err := rand.Int(rand.Reader, big.NewInt(int64(size)))
	if err != nil {
		return 0, err
	}
	return int(index.Int64()), nil
}

func withRandomBenchmarkPolicy(s *proxy.Service) *proxy.Service {
	return s.WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyRandomBenchmark, Router: randomrouter.New(randomBenchmarkDraw), Unavailable: randomrouter.ErrUnavailable})
}
