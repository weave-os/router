package candidates_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
)

func set(models ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(models))
	for _, m := range models {
		out[m] = struct{}{}
	}
	return out
}

func TestExcludedModels(t *testing.T) {
	gatewayCalled := false
	unserved := func() map[string]struct{} {
		gatewayCalled = true
		return set("gpt-5-mini")
	}

	got := candidates.ExcludedModels(candidates.ExclusionSources{
		Override:          set("claude-opus-5"),
		ProductIneligible: set("claude-haiku-4-5"),
		GatewayUnserved:   unserved,
	})
	assert.Equal(t, set("claude-opus-5", "claude-haiku-4-5"), got)
	assert.False(t, gatewayCalled, "an operator override skips the per-request sources")

	got = candidates.ExcludedModels(candidates.ExclusionSources{
		InstallationExcluded: []string{"claude-sonnet-4-6"},
		Allowed:              set("claude-opus-5"),
		Available:            set("claude-opus-5", "claude-haiku-4-5"),
		GatewayUnserved:      unserved,
	})
	assert.Equal(t, set("claude-sonnet-4-6", "claude-haiku-4-5", "gpt-5-mini"), got)

	assert.Nil(t, candidates.ExcludedModels(candidates.ExclusionSources{}))
}

func TestIntersectAllowed(t *testing.T) {
	assert.Equal(t, set("a"), candidates.IntersectAllowed(set("a"), nil))
	assert.Equal(t, set("b"), candidates.IntersectAllowed(nil, set("b")))
	assert.Equal(t, set("b"), candidates.IntersectAllowed(set("a", "b"), set("b", "c")))
	assert.Equal(t, set(), candidates.IntersectAllowed(set("a"), set()))
}

func TestPreferredModels(t *testing.T) {
	assert.Nil(t, candidates.PreferredModels(nil, nil))
	assert.Equal(t, []string{"sub-a", "pref-b"}, candidates.PreferredModels([]string{"sub-a"}, []string{"sub-a", "pref-b"}))
}

func TestRestrictToTierFallsBackWhenNoInTierCandidate(t *testing.T) {
	excluded := set("deepseek/deepseek-v4-pro")
	out, ok := candidates.RestrictToTier(excluded, catalog.TierHigh, set("claude-haiku-4-5"))
	assert.False(t, ok, "no high-tier model is available, so the constraint must not apply")
	assert.Equal(t, excluded, out, "the original denylist is returned unchanged on fallback")
}

func TestRestrictToTierExcludesOtherTiers(t *testing.T) {
	out, ok := candidates.RestrictToTier(nil, catalog.TierHigh, set("claude-opus-5", "claude-haiku-4-5", "claude-sonnet-4-6"))
	require.True(t, ok)
	assert.Equal(t, set("claude-haiku-4-5", "claude-sonnet-4-6"), out)
}

func TestExcludeCodexOAuthOnlyModels(t *testing.T) {
	none := func(string) bool { return false }
	all := func(string) bool { return true }
	assert.Nil(t, candidates.ExcludeCodexOAuthOnlyModels(nil, nil, all))

	got := candidates.ExcludeCodexOAuthOnlyModels(set("openai"), nil, none)
	require.NotEmpty(t, got)
	for model := range got {
		m, known := catalog.ByID(model)
		require.True(t, known)
		require.NotEmpty(t, m.Providers)
		assert.Equal(t, "openai", m.Providers[0].Provider, "%s is served first by OpenAI", model)
	}
}
