package proxy_test

import (
	"context"
	"testing"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/hmm"

	"github.com/stretchr/testify/assert"
)

// newFlagTestService builds a Service with no providers wired: these tests only
// exercise flag resolution, which touches nothing but the Service's flag fields
// and the request context.
func newFlagTestService() *proxy.Service {
	return proxy.WithStrategyPorts(proxy.NewService(
		nil, map[string]providers.Client{}, nil, nil,
		false, providers.ProviderAnthropic, "claude-haiku-4-5", nil), hmm.ServingPorts())

}

func TestResolversFallBackToDeploymentDefault(t *testing.T) {
	svc := newFlagTestService().
		WithTurnSignalCapture(true).
		WithTextRepetitionBreak(false).
		WithSiblingFailover(false)

	ctx := context.Background()
	assert.True(t, svc.ResolveTurnSignalCaptureEnabled(ctx))
	assert.False(t, svc.ResolveTextRepetitionBreakEnabled(ctx))
	assert.False(t, svc.ResolveSiblingFailover(ctx))
}

func TestPerOrgOverrideBeatsDeploymentDefault(t *testing.T) {
	svc := newFlagTestService().
		WithTurnSignalCapture(true).
		WithSiblingFailover(false).
		WithTextRepetitionBreak(true)

	ctx := flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{
			flags.KeyTurnSignalCapture: false,
			flags.KeySiblingFailover:   true,
		},
	})

	assert.False(t, svc.ResolveTurnSignalCaptureEnabled(ctx), "org override should turn a default-on flag off")
	assert.True(t, svc.ResolveSiblingFailover(ctx), "org override should turn a default-off flag on")

	// An unoverridden flag on the same context keeps its deployment default.
	assert.True(t, svc.ResolveTextRepetitionBreakEnabled(ctx))
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

func TestStringFlagOverride(t *testing.T) {
	svc := newFlagTestService().
		WithCyberRefusalRepin(true).
		WithCyberRefusalFallbackModel("claude-sonnet-5")

	assert.Equal(t, "claude-sonnet-5", svc.ResolveCyberRefusalFallbackModel(context.Background()))

	ctx := flags.WithOverrides(context.Background(), flags.Overrides{
		Strings: map[flags.Key]string{flags.KeyCyberRefusalFallback: "claude-opus-5"},
	})
	assert.Equal(t, "claude-opus-5", svc.ResolveCyberRefusalFallbackModel(ctx))
}
