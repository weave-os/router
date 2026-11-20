package proxy

import "weave-os/router/internal/router/continuity"

type hmmServing struct {
	hmmConfig     continuity.AuthoritativeConfig
	strategyPorts continuity.StrategyPorts
}

func WithStrategyPorts(s *Service, ports continuity.StrategyPorts) *Service {
	if err := ports.Validate(); err != nil {
		panic(err)
	}
	s.strategyPorts = ports
	return s
}
