package proxy

import (
	"context"
	"sort"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

type RoutingCatalog Service

func NewRoutingCatalog(s *Service) *RoutingCatalog {
	return (*RoutingCatalog)(s)
}

func (s *RoutingCatalog) HasExcludedModelsOverride() bool {
	return s.routing.ExcludedModelsOverride() != nil
}

func (s *RoutingCatalog) RoutableModels() map[string]struct{} {
	if s == nil {
		return nil
	}
	universe := (*Service)(s).routableUniverse()
	out := make(map[string]struct{}, len(universe))
	for m := range universe {
		out[m] = struct{}{}
	}
	return out
}

func (s *RoutingCatalog) ExcludedModelsOverride() []string {
	override := s.routing.ExcludedModelsOverride()
	if override == nil {
		return nil
	}
	out := make([]string, 0, len(override))
	for m := range override {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func (s *RoutingCatalog) HasExcludedProvidersOverride() bool {
	return s.credentials.ExcludedProvidersOverride != nil
}

func (s *RoutingCatalog) ExcludedProvidersOverride() []string {
	if s.credentials.ExcludedProvidersOverride == nil {
		return nil
	}
	out := make([]string, 0, len(s.credentials.ExcludedProvidersOverride))
	for p := range s.credentials.ExcludedProvidersOverride {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (s *RoutingCatalog) PolicyCapabilities(strategy router.Strategy) (policy.Capabilities, bool) {
	registered, ok := s.strategies[strategy]
	if ok {
		if source, dynamic := registered.router.(policy.CapabilitySource); dynamic {
			return source.CurrentCapabilities(), true
		}
	}
	return registered.capabilities, ok
}

func (s *RoutingCatalog) PolicyCapabilitiesForRequest(ctx context.Context, strategy router.Strategy) (policy.Capabilities, bool) {
	if source, ok := s.strategies[strategy].router.(policy.RequestCapabilitySource); ok {
		return source.CapabilitiesForRequest(ctx), true
	}
	return s.PolicyCapabilities(strategy)
}

func (s *RoutingCatalog) PolicyStrategyAvailable(strategy router.Strategy) bool {
	registered, ok := s.strategies[strategy]
	if !ok || registered.router == nil {
		return false
	}
	if source, dynamic := registered.router.(policy.AvailabilitySource); dynamic {
		return source.Available()
	}
	return true
}

func (s *RoutingCatalog) RegisteredStrategies() []router.Strategy {
	if s == nil {
		return nil
	}
	strategies := make([]router.Strategy, 0, len(s.strategies))
	for strategy := range s.strategies {
		strategies = append(strategies, strategy)
	}
	sort.Slice(strategies, func(i, j int) bool { return strategies[i] < strategies[j] })
	return strategies
}
