package proxy

import (
	"context"
	"errors"
	"testing"
	"weave-os/router/internal/dispatch"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compatibilityService(mode TranslationCompatibilityMode) *Service {
	return &Service{
		clients: dispatch.NewClients(map[string]providers.Client{
			providers.ProviderAnthropic: nil,
			providers.ProviderOpenAI:    nil,
			providers.ProviderGoogle:    nil,
		}),
		translationCompatibilityMode: mode,
	}
}

func TestTranslationPlan_NativeResponsesFiltersToOpenAIFamilyInShadow(t *testing.T) {
	svc := compatibilityService(TranslationCompatibilityShadow)
	plan := svc.planTranslation(router.Request{
		EnabledProviders: map[string]struct{}{
			providers.ProviderAnthropic: {},
			providers.ProviderOpenAI:    {},
		},
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat: router.WireFormatOpenAI,
			Endpoint:     router.EndpointOpenAIResponses,
			CustomTools:  true,
			NativeOnly:   true,
		},
	})

	assert.Equal(t, map[string]struct{}{providers.ProviderOpenAI: {}}, plan.EnabledProviders)
	assert.Equal(t, providers.FamilyOpenAICompat, plan.TargetFamily)
	requireExclusion(t, plan, "native_wire_family_required", providers.ProviderAnthropic, true)
}

func TestTranslationPlan_GeminiIngressNeverOffersForeignFamily(t *testing.T) {
	svc := compatibilityService(TranslationCompatibilityShadow)
	plan := svc.planTranslation(router.Request{
		EnabledProviders: map[string]struct{}{
			providers.ProviderAnthropic: {},
			providers.ProviderGoogle:    {},
		},
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat: router.WireFormatGemini,
			Endpoint:     router.EndpointGeminiGenerate,
		},
	})

	assert.Equal(t, map[string]struct{}{providers.ProviderGoogle: {}}, plan.EnabledProviders)
	requireExclusion(t, plan, "native_wire_family_required", providers.ProviderAnthropic, true)
}

func TestTranslationPlan_ImageConstraintShadowsBeforeEnforcement(t *testing.T) {
	req := router.Request{TranslationRequirements: router.TranslationRequirements{Images: true}}
	shadow := compatibilityService(TranslationCompatibilityShadow).planTranslation(req)
	enforce := compatibilityService(TranslationCompatibilityEnforce).planTranslation(req)

	assert.Empty(t, shadow.ExcludedModels, "shadow mode must preserve the pre-change candidate set")
	_, shadowReported := shadow.ExcludedModels["z-ai/glm-5"]
	assert.False(t, shadowReported)
	_, enforced := enforce.ExcludedModels["z-ai/glm-5"]
	assert.True(t, enforced, "known text-only models are hard excluded in enforce mode")
}

func TestTranslationPlan_OffRestoresNativeFamilyEligibility(t *testing.T) {
	plan := compatibilityService(TranslationCompatibilityOff).planTranslation(router.Request{
		EnabledProviders: map[string]struct{}{
			providers.ProviderAnthropic: {},
			providers.ProviderOpenAI:    {},
		},
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat: router.WireFormatOpenAI,
			Endpoint:     router.EndpointOpenAIResponses,
			NativeOnly:   true,
		},
	})

	assert.Equal(t, map[string]struct{}{
		providers.ProviderAnthropic: {},
		providers.ProviderOpenAI:    {},
	}, plan.EnabledProviders)
	requireExclusion(t, plan, "native_wire_family_required", providers.ProviderAnthropic, false)
}

func TestTranslationPlan_NativeResponsesRequireOpenAIResponsesAdapter(t *testing.T) {
	svc := compatibilityService(TranslationCompatibilityShadow)
	plan := svc.planTranslation(router.Request{
		EnabledProviders: map[string]struct{}{
			providers.ProviderOpenAI:     {},
			providers.ProviderOpenRouter: {},
		},
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat: router.WireFormatOpenAI,
			Endpoint:     router.EndpointOpenAIResponses,
			NativeOnly:   true,
		},
	})

	assert.Equal(t, map[string]struct{}{providers.ProviderOpenAI: {}}, plan.EnabledProviders)
	requireExclusion(t, plan, "native_wire_family_required", providers.ProviderOpenRouter, true)
}

func TestTranslationPlan_BroadSemanticRequirementOnlyFiltersInEnforce(t *testing.T) {
	req := router.Request{
		EnabledProviders: map[string]struct{}{
			providers.ProviderAnthropic: {},
			providers.ProviderOpenAI:    {},
		},
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat:       router.WireFormatAnthropic,
			Endpoint:           router.EndpointAnthropicMessages,
			PromptCacheControl: true,
		},
	}
	shadow := compatibilityService(TranslationCompatibilityShadow).planTranslation(req)
	enforce := compatibilityService(TranslationCompatibilityEnforce).planTranslation(req)

	assert.Equal(t, req.EnabledProviders, shadow.EnabledProviders)
	requireExclusion(t, shadow, "prompt_cache_control_native_required", providers.ProviderOpenAI, false)
	assert.Equal(t, map[string]struct{}{providers.ProviderAnthropic: {}}, enforce.EnabledProviders)
	requireExclusion(t, enforce, "prompt_cache_control_native_required", providers.ProviderOpenAI, true)
}

func TestTranslationPlan_NativeSearchAlwaysFiltersToSourceProvider(t *testing.T) {
	for _, mode := range []TranslationCompatibilityMode{
		TranslationCompatibilityOff,
		TranslationCompatibilityShadow,
		TranslationCompatibilityEnforce,
	} {
		t.Run(string(mode), func(t *testing.T) {
			plan := compatibilityService(mode).planTranslation(router.Request{
				EnabledProviders: map[string]struct{}{
					providers.ProviderAnthropic: {},
					providers.ProviderOpenAI:    {},
				},
				TranslationRequirements: router.TranslationRequirements{
					SourceFormat:      router.WireFormatAnthropic,
					Endpoint:          router.EndpointAnthropicMessages,
					CitationsOrSearch: true,
				},
			})

			assert.True(t, plan.Enforced)
			assert.Equal(t, map[string]struct{}{providers.ProviderAnthropic: {}}, plan.EnabledProviders)
			requireExclusion(t, plan, "citations_or_search_native_required", providers.ProviderOpenAI, true)
		})
	}
}

func TestTranslationPlan_MidConversationSystemRequirementsAreAlwaysEnforced(t *testing.T) {
	for _, mode := range []TranslationCompatibilityMode{
		TranslationCompatibilityOff,
		TranslationCompatibilityShadow,
		TranslationCompatibilityEnforce,
	} {
		t.Run(string(mode), func(t *testing.T) {
			plan := compatibilityService(mode).planTranslation(router.Request{
				EnabledProviders: map[string]struct{}{
					providers.ProviderAnthropic: {},
					providers.ProviderOpenAI:    {},
				},
				TranslationRequirements: router.TranslationRequirements{
					SourceFormat:                  router.WireFormatAnthropic,
					Endpoint:                      router.EndpointAnthropicMessages,
					MidConversationSystemMessages: true,
					MidConversationToolChanges:    true,
				},
			})

			assert.True(t, plan.Enforced)
			assert.Equal(t, map[string]struct{}{providers.ProviderAnthropic: {}}, plan.EnabledProviders)
			assert.NotContains(t, plan.ExcludedModels, "claude-opus-5")
			assert.Contains(t, plan.ExcludedModels, "claude-sonnet-5")
			assert.Contains(t, plan.SafetyExcludedModels, "claude-sonnet-5")
			requireExclusion(t, plan, "mid_conversation_tool_changes_native_required", providers.ProviderOpenAI, true)
			requireModelExclusion(t, plan, midConversationToolRequirementCode, "claude-sonnet-5", true)
		})
	}
}

func TestTranslationPlan_OutputConfigUsesItsNarrowerModelRoster(t *testing.T) {
	plan := compatibilityService(TranslationCompatibilityOff).planTranslation(router.Request{
		EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat:                  router.WireFormatAnthropic,
			Endpoint:                      router.EndpointAnthropicMessages,
			MidConversationSystemMessages: true,
			MidConversationOutputConfig:   true,
		},
	})

	assert.NotContains(t, plan.ExcludedModels, "claude-opus-5")
	assert.NotContains(t, plan.ExcludedModels, "claude-fable-5-1")
	assert.Contains(t, plan.ExcludedModels, "claude-opus-4-8")
	assert.Contains(t, plan.ExcludedModels, "claude-fable-5")
}

func TestTranslationPlan_MidConversationCompatibilityGuardsPinsRescuesAndBindings(t *testing.T) {
	requirements := router.TranslationRequirements{
		SourceFormat:                  router.WireFormatAnthropic,
		Endpoint:                      router.EndpointAnthropicMessages,
		MidConversationSystemMessages: true,
		MidConversationToolChanges:    true,
	}
	req := router.Request{TranslationRequirements: requirements}

	assert.True(t, pinEligible(sessionpin.Pin{Model: "claude-opus-5", Provider: providers.ProviderAnthropic}, req))
	assert.False(t, pinEligible(sessionpin.Pin{Model: "claude-sonnet-5", Provider: providers.ProviderAnthropic}, req))
	assert.False(t, pinEligible(sessionpin.Pin{Model: "claude-opus-5", Provider: providers.ProviderAnthropicGateway}, req))

	bindings := filterTranslationCompatibleBindings([]catalog.ProviderBinding{
		{Provider: providers.ProviderAnthropic},
		{Provider: providers.ProviderAnthropicGateway},
		{Provider: providers.ProviderOpenAIGateway},
	}, "claude-opus-5", requirements)
	assert.Equal(t, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, bindings)

	svc := siblingService(providers.ProviderAnthropic)
	rescue, found := svc.siblingFailoverDecision(context.Background(), router.Decision{
		Model:    "gpt-5.5",
		Provider: providers.ProviderOpenAI,
		Metadata: &router.RoutingMetadata{
			CandidateModels: []string{"claude-sonnet-5", "claude-opus-5"},
			CandidateProviders: map[string]string{
				"claude-sonnet-5": providers.ProviderAnthropic,
				"claude-opus-5":   providers.ProviderAnthropic,
			},
		},
	}, requirements, 1_000, 0, 0)
	require.True(t, found)
	assert.Equal(t, "claude-opus-5", rescue.Model)
}

func TestApplyTranslationPlan_MidConversationModelUnavailable(t *testing.T) {
	svc := compatibilityService(TranslationCompatibilityOff)
	svc.availableModels = map[string]struct{}{"claude-sonnet-5": {}}
	_, err := svc.applyTranslationPlan(context.Background(), router.Request{
		EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat:                  router.WireFormatAnthropic,
			Endpoint:                      router.EndpointAnthropicMessages,
			MidConversationSystemMessages: true,
		},
	})

	assert.ErrorIs(t, err, ErrTranslationCompatibleProviderUnavailable)
}

func TestApplyTranslationPlan_CompatibleButUnavailable(t *testing.T) {
	svc := &Service{
		clients:                      dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: nil}),
		translationCompatibilityMode: TranslationCompatibilityShadow,
	}
	_, err := svc.applyTranslationPlan(context.Background(), router.Request{
		EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat: router.WireFormatOpenAI,
			Endpoint:     router.EndpointOpenAIResponses,
			NativeOnly:   true,
		},
	})

	assert.ErrorIs(t, err, ErrTranslationCompatibleProviderUnavailable)
}

func TestApplyTranslationPlan_IntrinsicallyIncompatible(t *testing.T) {
	svc := compatibilityService(TranslationCompatibilityEnforce)
	_, err := svc.applyTranslationPlan(context.Background(), router.Request{
		TranslationRequirements: router.TranslationRequirements{NativeOnly: true},
	})

	assert.True(t, errors.Is(err, ErrTranslationIntrinsicallyIncompatible))
}

func requireExclusion(t *testing.T, plan TranslationPlan, code, provider string, enforced bool) {
	t.Helper()
	for _, exclusion := range plan.Exclusions {
		if exclusion.Code == code && exclusion.Provider == provider {
			assert.Equal(t, enforced, exclusion.Enforced)
			return
		}
	}
	t.Fatalf("missing exclusion code=%q provider=%q in %#v", code, provider, plan.Exclusions)
}

func requireModelExclusion(t *testing.T, plan TranslationPlan, code, model string, enforced bool) {
	t.Helper()
	for _, exclusion := range plan.Exclusions {
		if exclusion.Code == code && exclusion.Model == model {
			assert.Equal(t, enforced, exclusion.Enforced)
			return
		}
	}
	t.Fatalf("missing exclusion code=%q model=%q in %#v", code, model, plan.Exclusions)
}
