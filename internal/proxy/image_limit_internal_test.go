package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/credentials"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func TestResolveBindingsForDispatchHonorsImageCount(t *testing.T) {
	model := "xiaomi/mimo-v2.6-pro"
	s := &Service{hmmServing: hmmPorts(), credentials: credentials.Brokerage{KeyedProviders: map[string]struct{}{
		providers.ProviderDeepInfra:  {},
		providers.ProviderOpenRouter: {},
	}}}
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
	s := &Service{hmmServing: hmmPorts(), credentials: credentials.Brokerage{KeyedProviders: map[string]struct{}{providers.ProviderDeepInfra: {}}}}
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
	s := &Service{hmmServing: hmmPorts(), credentials: credentials.Brokerage{KeyedProviders: map[string]struct{}{providers.ProviderOpenRouter: {}}}}
	decision := router.Decision{Model: "xiaomi/mimo-v2.6-pro", Provider: providers.ProviderOpenRouter}
	large := s.openRouterOverflowBindings(withRequestImageCount(context.Background(), 17), decision)
	require.Len(t, large, 1)
	assert.Equal(t, providers.ProviderOpenRouter, large[0].Provider)
	require.Len(t, s.openRouterOverflowBindings(withRequestImageCount(context.Background(), 3), decision), 1)
}
