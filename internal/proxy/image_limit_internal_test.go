package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func TestExcludeImageLimitModels(t *testing.T) {
	model := "xiaomi/mimo-v2.6-pro"
	textOnly := "qwen/qwen3-235b-a22b-2507"
	available := map[string]struct{}{model: {}, textOnly: {}}

	t.Run("deepinfra only excludes capped model but keeps text-only soft filter", func(t *testing.T) {
		excluded, removed := excludeImageLimitModels(17, map[string]struct{}{providers.ProviderDeepInfra: {}}, nil, nil, available)
		assert.Equal(t, []string{model}, removed)
		assert.Equal(t, map[string]struct{}{model: {}}, excluded)
	})
	t.Run("openrouter binding keeps model eligible", func(t *testing.T) {
		excluded, removed := excludeImageLimitModels(17, map[string]struct{}{providers.ProviderOpenRouter: {}}, nil, nil, available)
		assert.Nil(t, excluded)
		assert.Empty(t, removed)
	})
	t.Run("custom binding has no known cap", func(t *testing.T) {
		excluded, removed := excludeImageLimitModels(
			17,
			map[string]struct{}{providers.ProviderDeepInfra: {}},
			map[string][]string{model: {"custom"}},
			nil,
			available,
		)
		assert.Nil(t, excluded)
		assert.Empty(t, removed)
	})
	t.Run("existing exclusions are preserved copy-on-write", func(t *testing.T) {
		existing := map[string]struct{}{"already-excluded": {}}
		excluded, removed := excludeImageLimitModels(
			17,
			map[string]struct{}{providers.ProviderDeepInfra: {}},
			nil,
			existing,
			map[string]struct{}{model: {}},
		)
		assert.Equal(t, map[string]struct{}{"already-excluded": {}, model: {}}, excluded)
		assert.Equal(t, map[string]struct{}{"already-excluded": {}}, existing)
		assert.Equal(t, []string{model}, removed)
	})
}

func TestSafetyExcludedModelsIncludesImageLimits(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"one"}},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"two"}},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"three"}},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"four"}},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"five"}}
	]}]}`))
	require.NoError(t, err)
	s := &Service{availableModels: map[string]struct{}{"xiaomi/mimo-v2.6-pro": {}}}
	excluded := s.safetyExcludedModels(env, 0, map[string]struct{}{providers.ProviderDeepInfra: {}}, nil)
	assert.Equal(t, map[string]struct{}{"xiaomi/mimo-v2.6-pro": {}}, excluded)
}

func TestResolveBindingsForDispatchHonorsImageCount(t *testing.T) {
	model := "xiaomi/mimo-v2.6-pro"
	s := &Service{deploymentKeyedProviders: map[string]struct{}{
		providers.ProviderDeepInfra:  {},
		providers.ProviderOpenRouter: {},
	}}
	decision := router.Decision{Model: model, Provider: providers.ProviderDeepInfra}

	large := s.resolveBindingsForDispatch(withRequestImageCount(context.Background(), 17), decision)
	require.Len(t, large, 1)
	assert.Equal(t, providers.ProviderOpenRouter, large[0].Provider)

	small := s.resolveBindingsForDispatch(withRequestImageCount(context.Background(), 3), decision)
	require.GreaterOrEqual(t, len(small), 2)
	assert.Equal(t, providers.ProviderDeepInfra, small[0].Provider)
}

func TestResolveBindingsForDispatchKeepsExplicitForceWhenNoEligibleBinding(t *testing.T) {
	model := "xiaomi/mimo-v2.6-pro"
	s := &Service{deploymentKeyedProviders: map[string]struct{}{providers.ProviderDeepInfra: {}}}
	bindings := s.resolveBindingsForDispatch(
		withRequestImageCount(context.Background(), 17),
		router.Decision{Model: model, Provider: providers.ProviderDeepInfra, Reason: translate.ReasonUserForceModel},
	)
	require.Len(t, bindings, 1)
	assert.Equal(t, providers.ProviderDeepInfra, bindings[0].Provider)
}

func TestSiblingRescueSkipsBindingOverImageLimit(t *testing.T) {
	model := "xiaomi/mimo-v2.6-pro"
	var skipped rescueSkipReason
	decisions := walkRescueCandidates(
		router.Decision{Model: "claude-opus-5", Provider: providers.ProviderAnthropic},
		[]string{model},
		ReasonSiblingFailover,
		17,
		nil,
		nil,
		nil,
		0,
		0,
		0,
		func(string) (string, bool) { return providers.ProviderDeepInfra, true },
		func(_ string, reason rescueSkipReason) { skipped = reason },
	)
	assert.Empty(t, decisions)
	assert.Equal(t, rescueSkipImageLimit, skipped)
}

func TestOpenRouterOverflowBindingsAllowsUncappedImageCount(t *testing.T) {
	s := &Service{deploymentKeyedProviders: map[string]struct{}{providers.ProviderOpenRouter: {}}}
	decision := router.Decision{Model: "xiaomi/mimo-v2.6-pro", Provider: providers.ProviderOpenRouter}
	large := s.openRouterOverflowBindings(withRequestImageCount(context.Background(), 17), decision)
	require.Len(t, large, 1)
	assert.Equal(t, providers.ProviderOpenRouter, large[0].Provider)
	require.Len(t, s.openRouterOverflowBindings(withRequestImageCount(context.Background(), 3), decision), 1)
}
