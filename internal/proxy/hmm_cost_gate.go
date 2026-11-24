package proxy

import (
	"weave-os/router/internal/router/continuity"
)

func hmmCostGateFor(s *Service) continuity.CostGateConfig {
	return continuity.CostGateConfig{
		AvailableModels:            s.routing.AvailableModels(),
		RegisteredProviders:        func() map[string]struct{} { return s.engine.Clients().NameSet() },
		UpgradeConfidenceThreshold: s.hmmConfig.UpgradeConfidenceThreshold,
	}
}
