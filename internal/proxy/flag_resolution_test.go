package proxy_test

import (
	"context"
	"testing"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"

	"github.com/stretchr/testify/assert"
)

// newFlagTestService builds a Service with no providers wired: these tests only
// exercise flag resolution, which touches nothing but the Service's flag fields
// and the request context.
func newFlagTestService() *proxy.Service {
	return proxy.NewService(
		nil, map[string]providers.Client{}, nil, nil,
		false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	)
}

func TestResolversFallBackToDeploymentDefault(t *testing.T) {
	svc := newFlagTestService().
		WithStruggleShadowConfig(true).
		WithSpiralShadowConfig(false).
		WithLoopEscalationConfig(true, 10)

	ctx := context.Background()
	assert.True(t, svc.ResolveStruggleShadowEnabled(ctx))
	assert.False(t, svc.ResolveSpiralShadowEnabled(ctx))
	assert.True(t, svc.ResolveLoopEscalationEnabled(ctx))
	assert.Equal(t, 10, svc.ResolveLoopEscalationHoldoutPct(ctx))
}

func TestPerOrgOverrideBeatsDeploymentDefault(t *testing.T) {
	// The motivating case: the deployment ships the struggle detector on, and one
	// organization turns it off.
	svc := newFlagTestService().
		WithStruggleShadowConfig(true).
		WithLoopEscalationConfig(true, 10)

	ctx := flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{
			flags.KeyStruggleShadowEnabled: false,
		},
		Ints: map[flags.Key]int{flags.KeyLoopEscalationHoldoutPct: 100},
	})

	assert.False(t, svc.ResolveStruggleShadowEnabled(ctx), "org override should turn a default-on flag off")
	assert.Equal(t, 100, svc.ResolveLoopEscalationHoldoutPct(ctx))

	// An unoverridden flag on the same context keeps its deployment default.
	assert.True(t, svc.ResolveLoopEscalationEnabled(ctx))
}

func TestCCTaskToolsCrossVendorResolution(t *testing.T) {
	svc := newFlagTestService()
	assert.False(t, svc.ResolveCCTaskToolsCrossVendor(context.Background()),
		"the task-list tools stay stripped until an installation opts in")

	on := flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{flags.KeyCCTaskToolsCrossVendor: true},
	})
	assert.True(t, svc.ResolveCCTaskToolsCrossVendor(on))

	deploymentOn := newFlagTestService().WithCCTaskToolsCrossVendor(true)
	assert.True(t, deploymentOn.ResolveCCTaskToolsCrossVendor(context.Background()))
	off := flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{flags.KeyCCTaskToolsCrossVendor: false},
	})
	assert.False(t, deploymentOn.ResolveCCTaskToolsCrossVendor(off))
}
