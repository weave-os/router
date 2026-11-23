package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/breaker"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/telemetry"
)

type stubTranslationMetrics struct{ telemetry.TranslationMetrics }

func newDispatchDepsTestService(cfg dispatch.Config) *Service {
	return WithDeps(withHMMPorts(NewService(nil, dispatch.NewClients(nil), nil, nil, false, "anthropic", "claude-haiku-4-5", nil)),
		Deps{Dispatch: &cfg})
}

func TestWithDepsAppliesDispatchConfig(t *testing.T) {
	executor, err := dispatch.NewExecutor(dispatch.NewClients(nil))
	require.NoError(t, err)
	registry := breaker.New(breaker.Defaults(), nil)
	metrics := &stubTranslationMetrics{}
	deployment := policy.DeploymentPolicyConfig{RoutableModels: map[string]struct{}{"claude-haiku-4-5": {}}}
	rescue := dispatch.RescueConfig{
		SiblingFailover: true, EarlyStopRedo: flags.EarlyStopRedoOff, CyberRefusalRepin: true, CyberRefusalRetry: true,
		CyberRefusalFallbackModel: "claude-opus-5", AnthropicServerSideFallback: true,
	}

	s := newDispatchDepsTestService(dispatch.Config{
		Rescue:      rescue,
		ArmDemotion: dispatch.ArmDemotionPolicy{CommittedStream: true, RescuedFailure: true},
		Throttling: dispatch.Throttling{
			Overload:  dispatch.NewOverloadBreaker(true, registry),
			RateLimit: dispatch.NewRateLimitPolicy(true, 42),
		},
		Executor:                     executor,
		Deployment:                   deployment,
		TranslationCompatibilityMode: TranslationCompatibilityEnforce,
		TranslationMetrics:           metrics,
	})

	assert.Equal(t, rescue, s.rescueConfig)
	assert.Equal(t, dispatch.ArmDemotionPolicy{CommittedStream: true, RescuedFailure: true}, s.armDemotion)
	assert.Equal(t, dispatch.NewOverloadBreaker(true, registry), s.throttling.Overload)
	assert.True(t, s.throttling.RateLimit.Transient(context.Background()))
	assert.Equal(t, 42, int(s.throttling.RateLimit.Cooldown(context.Background()).Seconds()))
	assert.Same(t, executor, s.engine.Executor())
	assert.Equal(t, deployment, s.engine.Deployment())
	assert.Equal(t, TranslationCompatibilityEnforce, s.translationCompatibilityMode)
	assert.Same(t, metrics, s.translationMetrics)
}

func TestWithDepsKeepsDispatchDefaultsForInvalidInputs(t *testing.T) {
	base := newDispatchDepsTestService(dispatch.Config{})
	s := newDispatchDepsTestService(dispatch.Config{
		Rescue:                       dispatch.RescueConfig{CyberRefusalFallbackModel: "  "},
		TranslationCompatibilityMode: "unknown",
	})

	assert.Equal(t, "claude-sonnet-5", s.rescueConfig.CyberRefusalFallbackModel, "a blank fallback model keeps the constructor default")
	assert.Equal(t, base.translationCompatibilityMode, s.translationCompatibilityMode, "an unknown translation mode is ignored")
}
