package proxy

import "weave-os/router/internal/router/hmm"

func withHMMPorts(s *Service) *Service { return WithStrategyPorts(s, hmm.ServingPorts()) }

func hmmPorts() strategyServing { return strategyServing{strategyPorts: hmm.ServingPorts()} }
