package proxy

import (
	"weave-os/router/internal/router/bandswap"
	"weave-os/router/internal/router/continuity"
)

type strategyServing struct {
	continuityConfig   continuity.Config
	hmmConfig          continuity.AuthoritativeConfig
	strategyPorts      continuity.StrategyPorts
	bandSwap           *bandswap.Classifier
	classifierSessions *ClassifierSessions
}

func WithStrategyPorts(s *Service, ports continuity.StrategyPorts) *Service {
	if err := ports.Validate(); err != nil {
		panic(err)
	}
	s.strategyPorts = ports
	return s
}
