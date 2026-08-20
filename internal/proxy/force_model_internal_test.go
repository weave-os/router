package proxy

import (
	"testing"

	"workweave/router/internal/providers"

	"github.com/stretchr/testify/assert"
)

func TestResolveForceModel(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantID       string
		wantProvider string
		wantKnown    bool
	}{
		// Catalog matches: provider comes from the primary binding, even
		// when the model name doesn't follow the bare-prefix heuristic. These
		// resolve to a real catalog entry, so known is true.
		{
			name:         "catalog anthropic",
			input:        "claude-opus-4-7",
			wantID:       "claude-opus-4-7",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    true,
		},
		{
			name:         "catalog google",
			input:        "gemini-3.1-flash-lite-preview",
			wantID:       "gemini-3.1-flash-lite-preview",
			wantProvider: providers.ProviderGoogle,
			wantKnown:    true,
		},
		{
			name:         "catalog bedrock — slash form",
			input:        "qwen/qwen3-235b-a22b-2507",
			wantID:       "qwen/qwen3-235b-a22b-2507",
			wantProvider: providers.ProviderBedrock,
			wantKnown:    true,
		},
		{
			name:         "catalog bedrock — bare suffix match",
			input:        "qwen3-235b-a22b-2507",
			wantID:       "qwen/qwen3-235b-a22b-2507",
			wantProvider: providers.ProviderBedrock,
			wantKnown:    true,
		},
		{
			name:         "alias gpt",
			input:        "gpt",
			wantID:       "gpt-5.6-sol",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    true,
		},
		{
			name:         "alias gpt hyphen minor version",
			input:        "gpt-5-5",
			wantID:       "gpt-5.5",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    true,
		},
		{
			name:         "native openai prefix",
			input:        "openai/gpt-5.6-luna",
			wantID:       "gpt-5.6-luna",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    true,
		},
		{
			name:         "native openai prefix with version alias",
			input:        "openai/gpt-5.6",
			wantID:       "gpt-5.6-sol",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    true,
		},
		{
			name:         "native openai prefix with model alias",
			input:        "openai/luna",
			wantID:       "gpt-5.6-luna",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    true,
		},
		{
			name:         "native openai prefix rejects cross-provider alias",
			input:        "openai/claude",
			wantID:       "claude",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    false,
		},
		{
			name:         "alias claude",
			input:        "claude",
			wantID:       "claude-opus-5",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    true,
		},
		{
			name:         "alias opus",
			input:        "opus",
			wantID:       "claude-opus-5",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    true,
		},
		{
			name:         "alias opus dotted version",
			input:        "opus-4.8",
			wantID:       "claude-opus-4-8",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    true,
		},
		{
			name:         "alias mixed case and whitespace",
			input:        "  Gemini  ",
			wantID:       "gemini-3-pro-preview",
			wantProvider: providers.ProviderGoogle,
			wantKnown:    true,
		},
		{
			name:         "alias qwen",
			input:        "qwen",
			wantID:       "qwen/qwen3-coder",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		// Heuristic fallback: not in the catalog, so known is false. The
		// provider is a best-effort guess for logging only; the handler rejects
		// these rather than pinning a model with no known tier.
		{
			name:         "heuristic openai — gpt-6 not in catalog",
			input:        "gpt-6",
			wantID:       "gpt-6",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    false,
		},
		{
			name:         "heuristic openai — o3",
			input:        "o3",
			wantID:       "o3",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    false,
		},
		{
			name:         "heuristic openrouter — unknown slash model",
			input:        "mistral/mistral-small-2603",
			wantID:       "mistral/mistral-small-2603",
			wantProvider: providers.ProviderOpenRouter,
			wantKnown:    false,
		},
		{
			name:         "unknown native openai prefix",
			input:        "openai/gpt-6",
			wantID:       "gpt-6",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    false,
		},
		{
			name:         "heuristic anthropic — unknown bareword",
			input:        "totally-not-a-model",
			wantID:       "totally-not-a-model",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    false,
		},
		// Truncated command (the bug this guard closes): "/force-model gpt-"
		// parses to "gpt-", which matches no catalog entry. The
		// separator-insensitive pass must not rescue it into the "gpt" alias —
		// a trailing separator means the user is mid-type, not punctuating.
		{
			name:         "truncated gpt- is not known",
			input:        "gpt-",
			wantID:       "gpt-",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    false,
		},
		{
			name:         "truncated qwen- is not known",
			input:        "qwen-",
			wantID:       "qwen-",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    false,
		},
		// Space-separated forms: users type the model the way they say it.
		// "/fm qwen 3.8" previously resolved to qwen/qwen3-coder — a different
		// model served under an ack that read as if the pin took.
		{
			name:         "space-separated qwen 3.8",
			input:        "qwen 3.8",
			wantID:       "qwen/qwen3.8-max",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "space-separated qwen max",
			input:        "qwen max",
			wantID:       "qwen/qwen3.8-max",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "space-separated full catalog id",
			input:        "qwen 3.8 max",
			wantID:       "qwen/qwen3.8-max",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "mixed case and spacing",
			input:        "Qwen 3.8 Max",
			wantID:       "qwen/qwen3.8-max",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "space-separated gpt 5.6 sol",
			input:        "gpt 5.6 sol",
			wantID:       "gpt-5.6-sol",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    true,
		},
		{
			name:         "space-separated claude opus 5",
			input:        "claude opus 5",
			wantID:       "claude-opus-5",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    true,
		},
		{
			name:         "space-separated kimi k3",
			input:        "kimi k3",
			wantID:       "moonshotai/kimi-k3",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotProvider, gotKnown := resolveForceModel(tt.input)
			assert.Equal(t, tt.wantID, gotID, "canonical id")
			assert.Equal(t, tt.wantProvider, gotProvider, "provider")
			assert.Equal(t, tt.wantKnown, gotKnown, "known")
		})
	}
}
