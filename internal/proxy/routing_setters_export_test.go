package proxy

import (
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/routingpolicy"
)

func (s *Service) WithContinuityConfig(cfg continuity.Config) *Service {
	s.continuityConfig = cfg
	return s
}

func (s *Service) WithHMMConfig(cfg continuity.AuthoritativeConfig) *Service {
	s.hmmConfig = cfg
	return s
}

func (s *Service) WithPolicyDeadlineFallback(enabled bool) *Service {
	deadline := s.routing.Deadline()
	deadline.Fallback = enabled
	s.routing = s.routing.WithDeadline(deadline)
	return s
}

func (s *Service) WithPolicyDeadlineDefaultModel(model string) *Service {
	deadline := s.routing.Deadline()
	deadline.DefaultModel = model
	s.routing = s.routing.WithDeadline(deadline)
	return s
}

func (s *Service) WithAvailableModels(models map[string]struct{}) *Service {
	s.routing = s.routing.WithAvailableModels(models)
	return s
}

func (s *Service) WithExplicitUtilityHardPin(enabled bool) *Service {
	hardPins := s.routing.HardPins()
	hardPins.ExplicitUtility = enabled
	s.routing = s.routing.WithHardPins(hardPins)
	return s
}

func (s *Service) WithHardPinResolver(resolver HardPinResolver) *Service {
	hardPins := s.routing.HardPins()
	hardPins.Resolver = resolver
	s.routing = s.routing.WithHardPins(hardPins)
	return s
}

func (s *Service) WithDefaultBaselineModel(model string) *Service {
	s.routing = s.routing.WithDefaultBaselineModel(model)
	return s
}

func (s *Service) WithSubAgentOverride(provider, model string) *Service {
	hardPins := s.routing.HardPins()
	hardPins.SubAgentProvider, hardPins.SubAgentModel = provider, model
	s.routing = s.routing.WithHardPins(hardPins)
	return s
}

func (s *Service) WithExcludedModelsOverride(models []string) *Service {
	s.routing = s.routing.WithExcludedModelsOverride(models)
	return s
}

func (s *Service) WithRoutingPolicy(p *routingpolicy.Policy) *Service {
	s.routing = s.routing.WithPlansFrom(p)
	return s
}

func (s *Service) WithGlobalAutomaticExclusions(store candidates.GlobalAutomaticExclusionStore) *Service {
	s.routing = s.routing.WithGlobalAutomaticExclusions(store)
	return s
}

func (s *Service) WithHMMRouter(r router.Router) *Service {
	return s.WithPolicyStrategy(policy.StrategySpec{
		Strategy:    router.StrategyHMM,
		Router:      r,
		Unavailable: router.ErrHMMUnavailable,
		Capabilities: policy.Capabilities{
			SchemaVersion:            policy.SchemaVersionV1,
			ReportsOutcomes:          true,
			ReportsFeedback:          true,
			HonorsPreferredModels:    true,
			HonorsQualityPriceBias:   true,
			SupportsDebugRouteDetail: true,
			SupportsShadow:           true,
		},
	})
}

func (s *Service) WithPolicyStrategy(spec policy.StrategySpec) *Service {
	return registerPolicyStrategy(s, spec)
}

func (s *Service) WithBandSwap(enabled bool) *Service {
	return withBandSwap(s, enabled)
}

func (s *Service) WithEscalation(store escalation.Store, observer escalation.Observer) *Service {
	s.escalationHelper().WithXGB(store, observer)
	return s
}

func (s *Service) WithLLMEscalation(store llmescalation.Store, judge llmescalation.Judge) *Service {
	s.escalationHelper().WithLLM(store, judge)
	return s
}

func (s *Service) WithQwenEscalation(judge llmescalation.Judge) *Service {
	s.escalationHelper().WithQwen(judge)
	return s
}

func (s *Service) WithEscalationShadowDefault(classifier flags.EscalationClassifier) *Service {
	s.escalationHelper().WithShadowDefault(classifier)
	return s
}

func (s *Service) WithClassifierSessions(config ClassifierSessionConfig, store router.ClassifierSessionStore, classifier router.AtomicClassifier) error {
	sessions, err := NewClassifierSessions(config, store, classifier)
	if err != nil {
		return err
	}
	s.classifierSessions = sessions
	return nil
}
