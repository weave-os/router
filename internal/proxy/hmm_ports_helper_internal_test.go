package proxy

import "weave-os/router/internal/router/hmm"

func withHMMPorts(s *Service) *Service { return WithStrategyPorts(s, hmm.ServingPorts()) }

func hmmPorts() hmmServing { return hmmServing{strategyPorts: hmm.ServingPorts()} }
