package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"
)

func TestAttachDispatchPlanFreezesNativeCandidate(t *testing.T) {
	s := &Service{
		providers: map[string]providers.Client{
			providers.ProviderOpenAI: nil,
		},
		deploymentKeyedProviders: map[string]struct{}{providers.ProviderOpenAI: {}},
	}
	req := router.Request{
		CustomBindings: map[string][]string{},
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat: router.WireFormatOpenAI,
			Endpoint:     router.EndpointOpenAIResponses,
		},
	}

	decision := s.attachDispatchPlan(context.Background(), req, router.Decision{
		Provider: providers.ProviderOpenAI,
		Model:    "gpt-5.4",
	})

	require.NotNil(t, decision.DispatchPlan)
	assert.True(t, decision.DispatchPlan.IsValid())
	assert.Equal(t, router.DispatchModeNative, decision.DispatchPlan.Candidate.Mode)
	assert.Equal(t, router.EndpointOpenAIResponses, decision.DispatchPlan.Candidate.Endpoint)
	assert.Equal(t, "gpt-5.4", decision.DispatchPlan.Candidate.UpstreamID)
	assert.True(t, decision.DispatchPlan.FallbackAllowed)
}

func TestAttachDispatchPlanUsesResponsesContextAndMarksTranslation(t *testing.T) {
	s := &Service{}
	req := router.Request{
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat: router.WireFormatOpenAI,
			Endpoint:     router.EndpointOpenAIChat,
		},
	}
	ctx := context.WithValue(context.Background(), responsesRequirementsContextKey{}, router.TranslationRequirements{
		SourceFormat: router.WireFormatOpenAI,
		Endpoint:     router.EndpointOpenAIResponses,
	})

	decision := s.attachDispatchPlan(ctx, req, router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-opus-5",
	})

	require.NotNil(t, decision.DispatchPlan)
	assert.Equal(t, router.EndpointOpenAIResponses, decision.DispatchPlan.Candidate.Endpoint)
	assert.Equal(t, router.DispatchModeTranslated, decision.DispatchPlan.Candidate.Mode)
	assert.Equal(t, "claude-opus-5", decision.DispatchPlan.Candidate.UpstreamID)
}

func TestDispatchDecisionForBindingRefreshesProviderAndUpstreamID(t *testing.T) {
	decision := router.Decision{
		Model:    "deepseek/deepseek-v4-pro",
		Provider: providers.ProviderFireworks,
		DispatchPlan: &router.DispatchPlan{
			Candidate: router.RoutingCandidate{
				Model:        "deepseek/deepseek-v4-pro",
				Provider:     providers.ProviderFireworks,
				UpstreamID:   "accounts/fireworks/models/deepseek-v4",
				SourceFormat: router.WireFormatAnthropic,
				Mode:         router.DispatchModeTranslated,
			},
			FallbackAllowed: true,
		},
	}

	got := dispatchDecisionForBinding(decision, catalog.ProviderBinding{
		Provider:   providers.ProviderOpenRouter,
		UpstreamID: "deepseek/deepseek-v4-pro",
	})

	assert.Equal(t, providers.ProviderOpenRouter, got.Provider)
	require.NotNil(t, got.DispatchPlan)
	assert.Equal(t, providers.ProviderOpenRouter, got.DispatchPlan.Candidate.Provider)
	assert.Equal(t, "deepseek/deepseek-v4-pro", got.DispatchPlan.Candidate.UpstreamID)
	assert.Equal(t, router.DispatchModeTranslated, got.DispatchPlan.Candidate.Mode)
	assert.True(t, got.DispatchPlan.FallbackAllowed)
}

func TestResolveBindingsForDispatchHonorsPlanFallbackPermission(t *testing.T) {
	s := &Service{deploymentKeyedProviders: map[string]struct{}{
		providers.ProviderFireworks:  {},
		providers.ProviderOpenRouter: {},
	}}
	decision := router.Decision{
		Model:    "deepseek/deepseek-v4-pro",
		Provider: providers.ProviderFireworks,
		DispatchPlan: &router.DispatchPlan{
			Candidate: router.RoutingCandidate{
				Model:      "deepseek/deepseek-v4-pro",
				Provider:   providers.ProviderFireworks,
				UpstreamID: "accounts/fireworks/models/deepseek-v4",
				Mode:       router.DispatchModeTranslated,
			},
			FallbackAllowed: false,
		},
	}

	bindings := s.resolveBindingsForDispatch(context.Background(), decision)
	require.Len(t, bindings, 1)
	assert.Equal(t, providers.ProviderFireworks, bindings[0].Provider)
	assert.Equal(t, "accounts/fireworks/models/deepseek-v4", bindings[0].UpstreamID)
}
