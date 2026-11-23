package proxy

import (
	"time"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
)

func (s *Service) WithByokOnly(byokOnly bool) *Service {
	s.credentials.BYOKOnly = byokOnly
	return s
}

func (s *Service) WithDeploymentKeyedProviders(set map[string]struct{}) *Service {
	if set == nil {
		s.credentials.KeyedProviders = nil
		return s
	}
	copied := make(map[string]struct{}, len(set))
	for p := range set {
		copied[p] = struct{}{}
	}
	s.credentials.KeyedProviders = copied
	return s
}

func (s *Service) WithExcludedProvidersOverride(providerNames []string) *Service {
	if len(providerNames) == 0 {
		s.credentials.ExcludedProvidersOverride = nil
		return s
	}
	set := make(map[string]struct{}, len(providerNames))
	for _, p := range providerNames {
		set[p] = struct{}{}
	}
	s.credentials.ExcludedProvidersOverride = set
	return s
}

func (s *Service) WithCodexExtraCredits(enabled bool) *Service {
	s.credentials.CodexExtraCredits = enabled
	return s
}

func (s *Service) WithManagedSubscriptions(pool subscriptions.Leaser) *Service {
	s.credentials.ManagedSubscriptions = pool
	return s
}

func (s *Service) WithUsageObserver(obs *usage.Observer) *Service {
	s.credentials.UsageObserver = obs
	return s
}

func (s *Service) WithClaudeSubscriptionPlanReader(reader providers.SubscriptionPlanReader) *Service {
	s.credentials.ClaudePlans = nil
	if reader != nil {
		s.credentials.ClaudePlans = subscriptions.NewPlanResolver(reader, func() time.Time { return s.clock.Now() })
	}
	return s
}

func (s *Service) WithExternalKeyUsageRecorder(recorder credentials.ExternalKeyUsageRecorder) *Service {
	s.credentials.ExternalKeyUsage = recorder
	return s
}
