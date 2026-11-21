package dispatch_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/breaker"
)

func overloadRegistry() *breaker.Registry {
	return breaker.New(breaker.Config{Strikes: 1, Window: time.Minute, OrgConfirmations: 1, RecoveryBase: time.Minute}, nil)
}

func TestOverloadBreakerWithdrawsModelAfterOverload(t *testing.T) {
	registry := overloadRegistry()
	b := dispatch.NewOverloadBreaker(true, registry)

	b.RecordOutcome(context.Background(), "org-a", "model-a", providers.ProviderAnthropic, false, &providers.UpstreamErrorResponse{Status: dispatch.ProviderOverloadedStatus})

	assert.Contains(t, b.OpenModels(context.Background(), "org-a"), "model-a")
	assert.Empty(t, b.OpenModels(context.Background(), "org-b"))
}

func TestOverloadBreakerIgnoresCommittedAndDisabledFailures(t *testing.T) {
	overloaded := &providers.UpstreamErrorResponse{Status: dispatch.ProviderOverloadedStatus}
	committed := dispatch.NewOverloadBreaker(true, overloadRegistry())
	committed.RecordOutcome(context.Background(), "org-a", "model-a", providers.ProviderAnthropic, true, overloaded)
	assert.Empty(t, committed.OpenModels(context.Background(), "org-a"))

	disabled := dispatch.NewOverloadBreaker(false, overloadRegistry())
	disabled.RecordOutcome(context.Background(), "org-a", "model-a", providers.ProviderAnthropic, false, overloaded)
	assert.Nil(t, disabled.OpenModels(context.Background(), "org-a"))
	assert.False(t, disabled.Armed(context.Background()))
}

func TestOverloadBreakerReadmitsHeldProbeModel(t *testing.T) {
	b := dispatch.NewOverloadBreaker(true, overloadRegistry())
	b.RecordOutcome(context.Background(), "org-a", "model-a", providers.ProviderAnthropic, false, &providers.UpstreamErrorResponse{Status: dispatch.ProviderOverloadedStatus})

	probe := breaker.Probe{Model: "model-a"}
	assert.NotContains(t, b.OpenModels(dispatch.WithOverloadProbe(context.Background(), probe), "org-a"), "model-a")
}

func TestRateLimitPolicyCooldownFallsBackToDefault(t *testing.T) {
	assert.Equal(t, time.Duration(dispatch.DefaultRateLimitCooldownSeconds)*time.Second, dispatch.NewRateLimitPolicy(true, 0).Cooldown(context.Background()))
	assert.Equal(t, 10*time.Second, dispatch.NewRateLimitPolicy(true, 10).Cooldown(context.Background()))
	require.True(t, dispatch.NewRateLimitPolicy(true, 10).Transient(context.Background()))
}
