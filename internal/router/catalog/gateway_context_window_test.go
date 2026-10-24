package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
)

func TestContextWindowForBindingPrefersMeasuredGatewayWindow(t *testing.T) {
	assert.Equal(t, 910_000, ContextWindowForBinding(ModelGPT6Luna, providers.ProviderOpenAIGateway))
	assert.Equal(t, ContextWindowFor(ModelGPT6Luna), ContextWindowForBinding(ModelGPT6Luna, providers.ProviderOpenAI))
	assert.Equal(t, 1_000_000, ContextWindowForBinding("claude-opus-5-5", providers.ProviderAnthropicGateway))
}
