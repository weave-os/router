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

func TestLookup_AnthropicMidConversationCapabilities(t *testing.T) {
	tests := []struct {
		model              string
		wantSystemMessages bool
		wantToolChanges    bool
		wantOutputConfig   bool
		wantTurnScoped     bool
	}{
		{"claude-opus-5", true, true, true, true},
		{"claude-fable-5-1", true, true, true, true},
		{"claude-opus-4-8", true, true, false, true},
		{"claude-fable-5", true, true, false, true},
		{"claude-sonnet-5", false, false, false, false},
		{"claude-opus-4-7", false, false, false, false},
	}

	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			spec := router.Lookup(test.model)
			assert.Equal(t, test.wantSystemMessages, spec.Supports(router.CapMidConversationSystemMessages))
			assert.Equal(t, test.wantToolChanges, spec.Supports(router.CapMidConversationToolChanges))
			assert.Equal(t, test.wantOutputConfig, spec.Supports(router.CapMidConversationOutputConfig))
			assert.Equal(t, test.wantTurnScoped, spec.Supports(router.CapTurnScopedSystemMessages))
		})
	}
}
