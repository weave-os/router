package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
)

func TestCortexGatewayContextWindowIsMeasured(t *testing.T) {
	w, ok := CortexGatewayContextWindow(ModelGPT6Luna, providers.ProviderOpenAIGateway)
	require.True(t, ok)
	assert.Equal(t, 910_000, w)
	w, ok = CortexGatewayContextWindow("claude-opus-5-5", providers.ProviderAnthropicGateway)
	require.True(t, ok)
	assert.Equal(t, 1_000_000, w)
	_, ok = CortexGatewayContextWindow(ModelGPT6Luna, providers.ProviderOpenAI)
	assert.False(t, ok)
}

func TestContextWindowForBindingKeepsCatalogWindowOnGateways(t *testing.T) {
	assert.Equal(t, ContextWindowFor(ModelGPT6Luna), ContextWindowForBinding(ModelGPT6Luna, providers.ProviderOpenAIGateway))
}
