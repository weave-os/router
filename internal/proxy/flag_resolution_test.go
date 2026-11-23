package proxy_test

import (
	"context"
	"testing"

	"weave-os/router/internal/dispatch"
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
		nil, dispatch.NewClients(map[string]providers.Client{}), nil, nil,
		false, providers.ProviderAnthropic, "claude-haiku-4-5", nil), hmm.ServingPorts())

}

func TestResolversFallBackToDeploymentDefault(t *testing.T) {
	svc := newFlagTestService().
		WithTextRepetitionBreak(false)

	ctx := context.Background()
	assert.False(t, svc.ResolveTextRepetitionBreakEnabled(ctx))
}

func TestPerOrgOverrideBeatsDeploymentDefault(t *testing.T) {
	svc := newFlagTestService().
		WithTextRepetitionBreak(true)

	ctx := flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{
			flags.KeyTurnSignalCapture: false,
			flags.KeySiblingFailover:   true,
		},
	})

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
