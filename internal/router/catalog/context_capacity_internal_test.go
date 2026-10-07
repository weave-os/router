package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"weave-os/router/internal/providers"
)

func TestEffectiveContextWindowForBinding(t *testing.T) {
	const opus = "claude-opus-4-7"
	for _, tc := range []struct {
		name, model, provider string
		extended              bool
		want                  int
	}{
		{"base", opus, providers.ProviderAnthropic, false, 200_000},
		{"extended", opus, providers.ProviderAnthropic, true, 1_000_000},
		{"messages gateway", opus, providers.ProviderAnthropicGateway, true, 1_000_000},
		{"chat gateway", opus, providers.ProviderOpenAIGateway, true, 200_000},
		{"unknown provider", opus, "unknown-provider", true, 200_000},
		{"unsupported model", "claude-haiku-4-5", providers.ProviderAnthropic, true, 200_000},
		{"dated model", opus + "-20260101", providers.ProviderAnthropic, true, 1_000_000},
		{"unknown model", "unknown-model", providers.ProviderAnthropic, true, DefaultContextWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, EffectiveContextWindowForBinding(tc.model, tc.provider, tc.extended))
		})
	}
}

func TestEffectiveContextWindowHonorsBindingOverride(t *testing.T) {
	const modelID = "claude-opus-4-7"
	original := byID[modelID]
	t.Cleanup(func() { byID[modelID] = original })
	changed := original
	changed.Providers = append([]ProviderBinding(nil), original.Providers...)
	changed.Providers[0].ContextWindow = 300_000
	byID[modelID] = changed
	assert.Equal(t, 300_000, EffectiveContextWindowForBinding(modelID, providers.ProviderAnthropic, true))
	assert.Equal(t, 1_000_000, EffectiveContextWindowForBinding(modelID, providers.ProviderAnthropicGateway, true))
	changed.ContextWindow = 0
	byID[modelID] = changed
	assert.Equal(t, DefaultContextWindow, EffectiveContextWindowForBinding(modelID, providers.ProviderOpenAIGateway, false))
}
