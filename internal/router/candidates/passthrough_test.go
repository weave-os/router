package candidates_test

import (
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
)

func TestNativeWebSearchPassthroughServesOnlyAnAnthropicBinding(t *testing.T) {
	model := string(catalog.ModelIDClaudeSonnet46)
	anthropicOnly := router.Request{EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}}}

	decision, ok := candidates.NativeWebSearchPassthrough(anthropicOnly, model)
	assert.True(t, ok)
	assert.Equal(t, router.Decision{Provider: providers.ProviderAnthropic, Model: model, Reason: candidates.NativeWebSearchPassthroughReason}, decision)

	excluded := anthropicOnly
	excluded.Candidates.ExcludedModels = map[string]struct{}{model: {}}
	_, ok = candidates.NativeWebSearchPassthrough(excluded, model)
	assert.False(t, ok)

	_, ok = candidates.NativeWebSearchPassthrough(router.Request{EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}}}, model)
	assert.False(t, ok)
}

func TestRequestedModelServableRejectsASessionDemotedModel(t *testing.T) {
	req := router.Request{RequestedModel: string(catalog.ModelIDClaudeSonnet46)}

	assert.True(t, candidates.RequestedModelServable(req, nil))
	assert.False(t, candidates.RequestedModelServable(req, []string{req.RequestedModel}))
}
