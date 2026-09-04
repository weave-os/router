package proxy

import (
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		{
			name:         "canonical qwen3.8-max with vendor prefix",
			input:        "qwen/qwen3.8-max",
			wantID:       "qwen/qwen3.8-max",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "dash spelling qwen/qwen-3.8-max",
			input:        "qwen/qwen-3.8-max",
			wantID:       "qwen/qwen3.8-max",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "dash spelling qwen-3.8-max",
			input:        "qwen-3.8-max",
			wantID:       "qwen/qwen3.8-max",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "dash spelling qwen-3.8",
			input:        "qwen-3.8",
			wantID:       "qwen/qwen3.8-max",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "gpt-6 alias resolves to Astra",
			input:        "gpt-6",
			wantID:       "gpt-6-astra",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    true,
		},
		// Heuristic fallback: not in the catalog, so known is false. The
		// provider is a best-effort guess for logging only; the handler rejects
		// these rather than pinning a model with no known tier.
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
			name:         "native openai gpt-6 alias resolves to Astra",
			input:        "openai/gpt-6",
			wantID:       "gpt-6-astra",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    true,
		},
		{
			name:         "heuristic anthropic — unknown bareword",
			input:        "totally-not-a-model",
			wantID:       "totally-not-a-model",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    false,
		},
		// Truncated command (the bug this guard closes): "/force-model gpt-"
		// parses to "gpt-", which matches no catalog entry.
		{
			name:         "truncated gpt- is not known",
			input:        "gpt-",
			wantID:       "gpt-",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    false,
		},
		// Matching is exact: a name that merely contains, or is contained by, a
		// real one is unknown. "qwen 3.8" is the reported bug — it used to
		// resolve through the bare "qwen" alias and silently serve qwen3-coder.
		{
			name:         "spaced model name is not known",
			input:        "qwen 3.8",
			wantID:       "qwen 3.8",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    false,
		},
		{
			name:         "spaced alias is not known",
			input:        "qwen max",
			wantID:       "qwen max",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    false,
		},
		{
			name:         "model name with a trailing prompt is not known",
			input:        "gpt-5 help me debug this",
			wantID:       "gpt-5 help me debug this",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    false,
		},
		{
			// A prefix of a real ID must not resolve to it. (Contrast
			// "claude-opus", which resolves only because it's an explicit
			// alias — deliberate shorthands still work, guesses don't.)
			name:         "prefix of a real id is not known",
			input:        "claude-sonnet-4",
			wantID:       "claude-sonnet-4",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    false,
		},
		{
			// The bare-name table is exact too: a tail fragment is not a tail.
			name:         "fragment of a bare name is not known",
			input:        "mimo",
			wantID:       "mimo",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    false,
		},
		{
			// The vendor prefix stays optional via an exact bare-name entry.
			name:         "bare name of a slash-form model",
			input:        "mimo-v2.5-pro",
			wantID:       "xiaomi/mimo-v2.5-pro",
			wantProvider: providers.ProviderOpenRouter,
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

// The bare-name table must stay unambiguous as models are added: a tail shared
// by two models, or one that collides with a full ID or an alias, would make a
// bare name resolve to an arbitrary winner — the silent-wrong-model failure
// exact matching exists to prevent. Such tails are dropped, not guessed.
func TestBareCatalogNames_Unambiguous(t *testing.T) {
	tails := make(map[string][]string)
	for _, m := range catalog.Models {
		if _, tail, ok := strings.Cut(m.ID, "/"); ok && len(m.Providers) > 0 {
			tails[tail] = append(tails[tail], m.ID)
		}
	}

	for tail, owners := range tails {
		mapped, listed := bareCatalogNames[tail]
		_, isFullID := catalog.ByID(tail)
		_, aliased := forceModelAliases[tail]

		if len(owners) > 1 || isFullID || aliased {
			assert.False(t, listed,
				"ambiguous tail %q (owners=%v full_id=%v aliased=%v) must not be a bare name",
				tail, owners, isFullID, aliased)
			continue
		}
		require.True(t, listed, "unambiguous tail %q must be reachable without its vendor prefix", tail)
		assert.Equal(t, owners[0], mapped)
	}

	// Every entry must name a real, servable model.
	for tail, id := range bareCatalogNames {
		m, ok := catalog.ByID(id)
		require.True(t, ok, "bare name %q maps to unknown model %q", tail, id)
		assert.NotEmpty(t, m.Providers, "bare name %q maps to unservable model %q", tail, id)
	}
}

// An alias must win over a bare catalog name, so a deliberate alias can never
// be shadowed by an incidental tail collision.
func TestBareCatalogNames_AliasesTakePrecedence(t *testing.T) {
	for alias := range forceModelAliases {
		_, shadowed := bareCatalogNames[alias]
		assert.False(t, shadowed, "alias %q must not also be a bare-name entry", alias)
	}
}

// grok-4.5 is retired; family aliases (grok, xai) now follow flagship 4.6, own-name pins still resolve exactly.
func TestResolveForceModel_GrokFamilyAlias(t *testing.T) {
	for _, input := range []string{"grok", "xai"} {
		t.Run(input, func(t *testing.T) {
			gotID, gotProvider, gotKnown := resolveForceModel(input)
			assert.Equal(t, "grok-4.6", gotID, "canonical id")
			assert.Equal(t, providers.ProviderXAI, gotProvider, "provider")
			assert.True(t, gotKnown, "known")
		})
	}
}

// An explicit :level suffix must survive resolution to its catalog model.
func TestResolveForceModel_EffortSuffixPreserved(t *testing.T) {
	gotID, _, gotKnown, gotEffort := resolveForceModelWithEffort("grok-4.6:high")
	assert.Equal(t, "grok-4.6", gotID, "canonical id")
	assert.True(t, gotKnown, "known")
	assert.Equal(t, "high", gotEffort, "effort")
}
