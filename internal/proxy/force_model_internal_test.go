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
			wantID:       "gpt-6.1-sol",
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
			name:         "explicit sol 6 remains pinned",
			input:        "gpt6sol",
			wantID:       "gpt-6-sol",
			wantProvider: providers.ProviderOpenAI,
			wantKnown:    true,
		},
		{
			name:         "sol follows 6.1",
			input:        "sol",
			wantID:       "gpt-6.1-sol",
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
			wantID:       "gpt-6-luna",
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
			wantID:       "claude-opus-5-5",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    true,
		},
		{
			name:         "alias opus",
			input:        "opus",
			wantID:       "claude-opus-5-5",
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
			wantID:       "gemini-3.1-pro-preview",
			wantProvider: providers.ProviderGoogle,
			wantKnown:    true,
		},
		{
			name:         "alias google",
			input:        "google",
			wantID:       "gemini-3.1-pro-preview",
			wantProvider: providers.ProviderGoogle,
			wantKnown:    true,
		},
		{
			name:         "alias gemini pro",
			input:        "gemini-pro",
			wantID:       "gemini-3.1-pro-preview",
			wantProvider: providers.ProviderGoogle,
			wantKnown:    true,
		},
		{
			name:         "alias gemini flash",
			input:        "gemini-flash",
			wantID:       "gemini-3.8-flash",
			wantProvider: providers.ProviderGoogle,
			wantKnown:    true,
		},
		{
			name:         "alias qwen",
			input:        "qwen",
			wantID:       "qwen/qwen3.8-max",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "alias qwen coder",
			input:        "qwen-coder",
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
			name:         "alias deepseek pro current release",
			input:        "deepseek-pro",
			wantID:       "deepseek/deepseek-v4-pro-0813",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "alias kimi current release",
			input:        "kimi",
			wantID:       "moonshotai/kimi-k3",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "kimi k3.1 version pin",
			input:        "kimi-k3.1",
			wantID:       "moonshotai/kimi-k3.1",
			wantProvider: providers.ProviderOpenRouter,
			wantKnown:    true,
		},
		{
			name:         "upstream kimi k3.1 pro spelling",
			input:        "moonshotai/kimi-k3.1-pro",
			wantID:       "moonshotai/kimi-k3.1",
			wantProvider: providers.ProviderOpenRouter,
			wantKnown:    true,
		},
		{
			name:         "explicit kimi k3 pin remains exact",
			input:        "kimi-k3",
			wantID:       "moonshotai/kimi-k3",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "explicit kimi 2.7 pin remains exact",
			input:        "kimi-k2.7",
			wantID:       "moonshotai/kimi-k2.7",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "alias glm current release",
			input:        "glm",
			wantID:       "z-ai/glm-5.3",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "alias zai current release",
			input:        "zai",
			wantID:       "z-ai/glm-5.3",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "alias z-ai current release",
			input:        "z-ai",
			wantID:       "z-ai/glm-5.3",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "explicit glm 5.1 pin remains exact",
			input:        "glm-5.1",
			wantID:       "z-ai/glm-5.1",
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
		// Spaces spell the same name as dashes ("qwen-3.8" and "qwen-max" are
		// aliases), so the whole multi-word name resolves, not its first word.
		{
			name:         "spaced model name resolves as one name",
			input:        "qwen 3.8",
			wantID:       "qwen/qwen3.8-max",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		{
			name:         "spaced alias resolves as one name",
			input:        "qwen max",
			wantID:       "qwen/qwen3.8-max",
			wantProvider: providers.ProviderFireworks,
			wantKnown:    true,
		},
		// Matching is exact: a name that merely contains, or is contained by, a
		// real one is unknown. Do not infer a version from a generic alias.
		{
			name:         "unknown version of a known family is not known",
			input:        "qwen 9.9",
			wantID:       "qwen 9.9",
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

// grok-4.5 is retired; family aliases (grok, xai) follow flagship 4.7, while own-name pins resolve exactly.
func TestResolveForceModel_GrokFamilyAlias(t *testing.T) {
	for _, input := range []string{"grok", "xai"} {
		t.Run(input, func(t *testing.T) {
			gotID, gotProvider, gotKnown := resolveForceModel(input)
			assert.Equal(t, "grok-4.7", gotID, "canonical id")
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

// Spellings users actually typed into /force-model that were rejected in prod.
// Each one differs from a real name only in separator spelling, the optional
// "claude-" prefix, Claude Code's [1m] tag, or an effort level written without
// the colon, so it must pin exactly the model it names.
func TestResolveForceModel_ObservedSpellings(t *testing.T) {
	tests := []struct {
		input      string
		wantID     string
		wantEffort string
	}{
		{"claude-opus-5.5", "claude-opus-5-5", ""},
		{"Claude-Opus-5.5", "claude-opus-5-5", ""},
		{"claude opus 5.5", "claude-opus-5-5", ""},
		{"opus 5.5", "claude-opus-5-5", ""},
		{"opus_5_5", "claude-opus-5-5", ""},
		{"claude-sonnet-5.5", "claude-sonnet-5-5", ""},
		{"claude-haiku-4.5", "claude-haiku-4-5", ""},
		{"claude-fable-5.1", "claude-fable-5-1", ""},
		{"opus-4-5", "claude-opus-4-5", ""},
		{"opus-4.7", "claude-opus-4-7", ""},
		{"claude-opus-5-5[1m]", "claude-opus-5-5", ""},
		{"claude-opus-5.5[1m]", "claude-opus-5-5", ""},
		{"claude-opus-5.5[1m] high", "claude-opus-5-5", "high"},
		{"claude-opus-5-5[1m]-max", "claude-opus-5-5", "max"},
		{"claude-opus-5-5[1m]:xhigh", "claude-opus-5-5", "xhigh"},
		{"gpt 6.1 sol", "gpt-6.1-sol", ""},
		{"gpt-5-6-luna", "gpt-5.6-luna", ""},
		{"gemini-3-1-pro-preview", "gemini-3.1-pro-preview", ""},
		{"glm 5.3", "z-ai/glm-5.3", ""},
		{"kimi-k2-7", "moonshotai/kimi-k2.7", ""},
		{"astra high", "gpt-6-astra", "high"},
		{"gpt-6-sol-max", "gpt-6-sol", "max"},
		{"gpt-6-astra-max", "gpt-6-astra", "max"},
		{"opus 5.5 xhigh", "claude-opus-5-5", "xhigh"},
		{"claude-opus-5.5:high", "claude-opus-5-5", "high"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotID, _, gotKnown, gotEffort := resolveForceModelWithEffort(tt.input)
			assert.True(t, gotKnown, "known")
			assert.Equal(t, tt.wantID, gotID, "canonical id")
			assert.Equal(t, tt.wantEffort, gotEffort, "effort")
		})
	}
}

// Rejected prod inputs that name no model must stay rejected: folding fixes
// spelling, it never guesses.
func TestResolveForceModel_ObservedUnknowns(t *testing.T) {
	for _, input := range []string{
		"opsu", "opuis", "opues-5.5", "gtp-6-sol", "sool", "sokl", "astar",
		"gpt-61.-sol", "sol.", "opus-5-", "kimi-k", "gpt-6-1", "gpt6-1",
		"gpt-5.6-astra", "codex-sol-6", "sol-6", "gpt-sol-6", "/force-model sol",
		"openai/gpt-oss-120b", "qwen/qwen3-32b", "gpt-", "gpt-5 help me debug this",
		"opus high help",
	} {
		t.Run(input, func(t *testing.T) {
			_, _, gotKnown, _ := resolveForceModelWithEffort(input)
			assert.False(t, gotKnown, "known")
		})
	}
}

// Folding must never let one spelling reach two models. Every accepted name
// whose folded spelling is shared only with names for the same model stays
// reachable; a fold shared across models is dropped.
func TestFoldedForceModelNames_Unambiguous(t *testing.T) {
	targets := make(map[string]map[string]bool)
	for name, id := range forceModelNames() {
		key := foldModelSpelling(name)
		if targets[key] == nil {
			targets[key] = make(map[string]bool)
		}
		targets[key][id] = true
	}
	for key, ids := range targets {
		got, listed := foldedForceModelNames[key]
		if len(ids) > 1 {
			assert.False(t, listed, "folded key %q is shared by %v and must not resolve", key, ids)
			continue
		}
		require.True(t, listed, "folded key %q must resolve", key)
		assert.True(t, ids[got], "folded key %q resolves to %q", key, got)
	}
}

// Every name an exact lookup accepts must resolve to the same model through
// its folded spelling, so folding can only add reach, never redirect it.
func TestFoldedForceModelNames_AgreeWithExactNames(t *testing.T) {
	for name, id := range forceModelNames() {
		if folded, ok := foldedForceModelNames[foldModelSpelling(name)]; ok {
			assert.Equal(t, id, folded, "name %q", name)
		}
	}
}

// The unknown-model hint must only name models /force-model accepts.
func TestForceModelExamples_AreResolvable(t *testing.T) {
	for _, example := range strings.Split(strings.ReplaceAll(forceModelExamples, ", or ", ", "), ", ") {
		_, _, known := resolveForceModel(example)
		assert.True(t, known, "example %q", example)
	}
}

func TestSuggestForceModel(t *testing.T) {
	tests := []struct{ input, want string }{
		{"opsu", "opus"},
		{"opuis", "opus"},
		{"opues-5.5", "opus-5-5"},
		{"gtp-6-sol", "gpt-6-sol"},
		{"sool", "sol"},
		{"sokl", "sol"},
		{"astar", "astra"},
		{"gpt-61.-sol", "gpt-6.1-sol"},
		{"sonet", "sonnet"},
		{"gemni", "gemini"},
		{"opsu:high", "opus:high"},
		// A different version number is a different model, not a typo.
		{"gpt-6-1", ""},
		{"gpt-5.6-astra", ""},
		{"haiku-4.6", ""},
		{"qwen/qwen3-32b", ""},
		{"fix the tests", ""},
		{"opus high help", ""},
		{"zzzz", ""},
		{"ab", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, suggestForceModel(tt.input))
		})
	}
}

// A hint is only useful if typing it pins a model; it must never name
// something /force-model would reject.
func TestSuggestForceModel_SuggestionsResolve(t *testing.T) {
	for _, input := range []string{
		"opsu", "opuis", "opues-5.5", "gtp-6-sol", "sool", "sokl", "astar", "gpt-61.-sol",
		"sol.", "opus-5-", "kimi-k", "gpt-", "sonet", "gemni", "opsu:high", "haiku-45", "glm5.3x",
	} {
		t.Run(input, func(t *testing.T) {
			_, _, inputKnown, _ := resolveForceModelWithEffort(input)
			require.False(t, inputKnown, "input must be unknown for a hint to apply")
			s := suggestForceModel(input)
			if s == "" {
				return
			}
			_, _, known, _ := resolveForceModelWithEffort(s)
			assert.True(t, known, "suggestion %q must resolve", s)
		})
	}
}
