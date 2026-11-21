package router_test

import (
	"testing"

	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
)

func TestLookup_UnknownModel(t *testing.T) {
	spec := router.Lookup("unknown-model-99")
	assert.False(t, spec.Supports(router.CapAdaptiveThinking))
	assert.False(t, spec.Supports(router.CapExtendedThinking))
	assert.False(t, spec.Supports(router.CapReasoning))
}

func TestLookup_DateSuffixNormalization(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		wantAdaptive  bool
		wantExtended  bool
		wantReasoning bool
	}{
		{"anthropic haiku dated", "claude-haiku-4-5-20251001", false, true, false},
		{"anthropic opus dated", "claude-opus-4-7-20260301", true, false, false},
		{"openai dated", "gpt-4o-2024-08-06", false, false, false},
		{"openai luna pro registered", "gpt-5.6-luna-pro", false, false, true},
		{"openai sol pro registered", "gpt-5.6-sol-pro", false, false, true},
		{"openai gpt-6 astra registered", "gpt-6-astra", false, false, true},
		{"google flash registered", "gemini-2.5-flash", false, false, false},
		{"google pro registered", "gemini-2.5-pro", false, false, false},
		{"openrouter qwen registered", "qwen/qwen3-coder-next", false, false, false},
		{"unknown with date suffix", "mystery-model-20250101", false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := router.Lookup(tc.model)
			assert.Equal(t, tc.wantAdaptive, spec.Supports(router.CapAdaptiveThinking))
			assert.Equal(t, tc.wantExtended, spec.Supports(router.CapExtendedThinking))
			assert.Equal(t, tc.wantReasoning, spec.Supports(router.CapReasoning))
		})
	}
}

func TestLookup_GPT6AstraReasoning(t *testing.T) {
	spec := router.Lookup("gpt-6-astra")
	assert.True(t, spec.Supports(router.CapReasoning))
	assert.True(t, spec.Supports(router.CapXhighEffort))
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, spec.Reasoning().Levels)
	assert.True(t, spec.Reasoning().SupportsBudget)
	assert.True(t, spec.Reasoning().AlwaysOn)
}

func TestLookup_Gemini4Reasoning(t *testing.T) {
	for _, tc := range []struct {
		model  string
		levels []string
		xhigh  bool
	}{
		{router.ModelGemini4Argon, []string{"low", "medium", "high"}, false},
		{router.ModelGemini4Neon, []string{"low", "medium", "high", "xhigh", "max"}, true},
	} {
		spec := router.Lookup(tc.model)
		assert.Equal(t, tc.levels, spec.Reasoning().Levels, tc.model)
		assert.True(t, spec.Reasoning().AlwaysOn, tc.model)
		assert.Equal(t, tc.xhigh, spec.Supports(router.CapXhighEffort), tc.model)
	}
}

func TestLookup_Haiku55AcceptsDisabledThinkingAndForcedTools(t *testing.T) {
	spec := router.Lookup("claude-haiku-5-5")
	assert.True(t, spec.Supports(router.CapAdaptiveThinking))
	assert.True(t, spec.Supports(router.CapXhighEffort))
	assert.False(t, spec.Supports(router.CapAutoToolChoiceOnly))
	assert.False(t, spec.Reasoning().AlwaysOn)
	assert.Equal(t, []string{"low", "medium", "high", "max", "xhigh"}, spec.Reasoning().Levels)
}

func TestLookup_GPT61SolRequiresReasoning(t *testing.T) {
	spec := router.Lookup("gpt-6.1-sol")
	assert.True(t, spec.Supports(router.CapReasoning))
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, spec.Reasoning().Levels)
	assert.True(t, spec.Reasoning().AlwaysOn)
}

func TestLookup_GPT62RequiresReasoning(t *testing.T) {
	for _, id := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		spec := router.Lookup(id)
		assert.True(t, spec.Supports(router.CapReasoning), id)
		assert.True(t, spec.Reasoning().AlwaysOn, id)
	}
}

func TestLookup_ThinkingOffSwitch(t *testing.T) {
	for model, want := range map[string]router.ThinkingOff{
		"claude-sonnet-5-5": router.ThinkingOffBetweenTools,
		"claude-opus-5-5":   router.ThinkingOffAdaptiveLowEffort,
		"claude-fable-5":    router.ThinkingOffAdaptiveLowEffort,
		"claude-fable-5-1":  router.ThinkingOffAdaptiveLowEffort,
		"claude-fable-5-2":  router.ThinkingOffAdaptiveLowEffort,
		"claude-mythos-5-2": router.ThinkingOffAdaptiveLowEffort,
		"claude-opus-5":     router.ThinkingOffDisabled,
		"claude-opus-4-8":   router.ThinkingOffDisabled,
		"claude-opus-4-7":   router.ThinkingOffDisabled,
		"claude-sonnet-5":   router.ThinkingOffDisabled,
		"claude-sonnet-4-6": router.ThinkingOffDisabled,
		"claude-opus-4-6":   router.ThinkingOffDisabled,
		"claude-haiku-5-5":  router.ThinkingOffDisabled,
		"claude-haiku-4-5":  router.ThinkingOffDisabled,
	} {
		assert.Equal(t, want, router.Lookup(model).Reasoning().ThinkingOff, model)
	}
}
