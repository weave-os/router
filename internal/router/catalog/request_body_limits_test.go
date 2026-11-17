package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
)

func TestProviderBindingAcceptsRequestBodyBytes(t *testing.T) {
	const anthropicLimit = 32 * 1024 * 1024
	assert.Equal(t, anthropicLimit, MaxRequestBodyBytesForProvider(providers.ProviderAnthropic))
	assert.Zero(t, MaxRequestBodyBytesForProvider(providers.ProviderOpenAI))

	anthropic := ProviderBinding{Provider: providers.ProviderAnthropic}
	assert.True(t, anthropic.AcceptsRequestBodyBytes(anthropicLimit))
	assert.False(t, anthropic.AcceptsRequestBodyBytes(anthropicLimit+1))

	openAI := ProviderBinding{Provider: providers.ProviderOpenAI}
	assert.True(t, openAI.AcceptsRequestBodyBytes(64*1024*1024))
}
