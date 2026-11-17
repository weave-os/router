package dispatch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

const (
	anthropicBoundModel   = "claude-sonnet-4-6"
	overAnthropicBodySize = 40 * 1024 * 1024
)

func TestOrderedBindingsRejectsAnthropicBindingForOversizedBody(t *testing.T) {
	req := dispatch.BindingRequest{
		Decision:         router.Decision{Model: anthropicBoundModel, Provider: providers.ProviderAnthropic},
		RequestBodyBytes: overAnthropicBodySize,
	}
	assert.Nil(t, dispatch.OrderedBindings(req))

	req.UserForced = true
	assert.Nil(t, dispatch.OrderedBindings(req), "a forced model still cannot exceed the provider's request size limit")

	req.RequestBodyBytes = 1024
	assert.Equal(t, []string{providers.ProviderAnthropic}, providersOf(dispatch.OrderedBindings(req)))
}

func TestOrderedBindingsBodySizeLeavingNoKeyedBindingHasNoWalk(t *testing.T) {
	got := dispatch.OrderedBindings(dispatch.BindingRequest{
		Decision:                 router.Decision{Model: anthropicBoundModel, Provider: providers.ProviderAnthropic},
		DeploymentKeyedProviders: keyed(providers.ProviderAnthropic),
		RequestBodyBytes:         overAnthropicBodySize,
	})
	assert.Nil(t, got)
}

func TestOrderedBindingsBodySizeKeepsUnlimitedBindings(t *testing.T) {
	got := dispatch.OrderedBindings(dispatch.BindingRequest{
		Decision:                 router.Decision{Model: multiBindingModel, Provider: providers.ProviderFireworks},
		DeploymentKeyedProviders: keyed(providers.ProviderFireworks, providers.ProviderOpenRouter),
		RequestBodyBytes:         overAnthropicBodySize,
	})
	assert.Equal(t, []string{providers.ProviderFireworks, providers.ProviderOpenRouter}, providersOf(got))
}
