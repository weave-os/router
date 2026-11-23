package proxy

import (
	"context"
	"strings"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/breaker"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/telemetry"
)

func (s *Service) WithTranslationCompatibilityMode(mode TranslationCompatibilityMode) *Service {
	switch mode {
	case TranslationCompatibilityOff, TranslationCompatibilityShadow, TranslationCompatibilityEnforce:
		s.translationCompatibilityMode = mode
	}
	return s
}

func (s *Service) WithCyberRefusalRepin(enabled bool) *Service {
	s.rescueConfig.CyberRefusalRepin = enabled
	return s
}

func (s *Service) WithCyberRefusalRetry(enabled bool) *Service {
	s.rescueConfig.CyberRefusalRetry = enabled
	return s
}

func (s *Service) WithAnthropicServerSideFallback(enabled bool) *Service {
	s.rescueConfig.AnthropicServerSideFallback = enabled
	return s
}

func (s *Service) WithSiblingFailover(enabled bool) *Service {
	s.rescueConfig.SiblingFailover = enabled
	return s
}

func (s *Service) WithCommittedStreamArmDemotion(enabled bool) *Service {
	s.armDemotion.CommittedStream = enabled
	return s
}

func (s *Service) WithRescuedFailureArmDemotion(enabled bool) *Service {
	s.armDemotion.RescuedFailure = enabled
	return s
}

func (s *Service) WithTransientRateLimit(enabled bool, cooldownSeconds int) *Service {
	s.throttling.RateLimit = dispatch.NewRateLimitPolicy(enabled, cooldownSeconds)
	return s
}

func (s *Service) WithRetrySleep(sleep func(context.Context, time.Duration) error) *Service {
	s.engine.SetRetrySleep(sleep)
	return s
}

func (s *Service) WithCyberRefusalFallbackModel(model string) *Service {
	if strings.TrimSpace(model) != "" {
		s.rescueConfig.CyberRefusalFallbackModel = strings.TrimSpace(model)
	}
	return s
}

func (s *Service) WithEarlyStopRedo(mode flags.EarlyStopRedoMode) *Service {
	s.rescueConfig.EarlyStopRedo = mode
	return s
}

func (s *Service) WithInferenceExecutor(executor *dispatch.Executor) *Service {
	s.engine.SetExecutor(executor)
	return s
}

func (s *Service) WithTranslationMetrics(metrics telemetry.TranslationMetrics) *Service {
	s.translationMetrics = metrics
	return s
}

func (s *Service) WithProviderOverloaded(enabled bool, registry *breaker.Registry) *Service {
	s.throttling.Overload = dispatch.NewOverloadBreaker(enabled, registry)
	return s
}

func (s *Service) WithInferencePlans(plans *policy.PlanResolver) *Service {
	s.engine.SetPlans(plans)
	return s
}

func (s *Service) WithInferenceDeployment(config policy.DeploymentPolicyConfig) *Service {
	s.engine.SetDeployment(config)
	return s
}
