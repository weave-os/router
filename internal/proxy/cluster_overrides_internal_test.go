package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMergeClusterOverrides_NeitherConfigured(t *testing.T) {
	assert.Nil(t, mergeClusterOverrides(nil, nil))
	assert.Nil(t, mergeClusterOverrides(map[string][]string{}, map[string][]string{}))
}

func TestMergeClusterOverrides_KeyOnly(t *testing.T) {
	key := map[string][]string{"balanced": {"a", "b"}}

	assert.Equal(t, key, mergeClusterOverrides(key, nil))
}

func TestMergeClusterOverrides_UserOnly(t *testing.T) {
	user := map[string][]string{"balanced": {"a", "b"}}

	assert.Equal(t, user, mergeClusterOverrides(nil, user))
}

// Clusters the user never configured must keep the org's list untouched.
func TestMergeClusterOverrides_DisjointClustersBothApply(t *testing.T) {
	got := mergeClusterOverrides(
		map[string][]string{"maximum": {"opus"}},
		map[string][]string{"fast": {"haiku"}},
	)

	assert.Equal(t, map[string][]string{
		"maximum": {"opus"},
		"fast":    {"haiku"},
	}, got)
}

// The core composition rule: a user narrows within the org's list, and their
// ordering wins for the models that survive.
func TestMergeClusterOverrides_IntersectsPreservingUserOrder(t *testing.T) {
	got := mergeClusterOverrides(
		map[string][]string{"balanced": {"a", "b", "c"}},
		map[string][]string{"balanced": {"c", "a"}},
	)

	assert.Equal(t, map[string][]string{"balanced": {"c", "a"}}, got)
}

// The privilege-escalation guard: a user must NOT be able to re-admit a model
// the org's key-scoped list deliberately removed. A plain override would.
func TestMergeClusterOverrides_UserCannotWidenPastOrgList(t *testing.T) {
	got := mergeClusterOverrides(
		map[string][]string{"balanced": {"approved"}},
		map[string][]string{"balanced": {"approved", "org-removed-this"}},
	)

	assert.Equal(t, map[string][]string{"balanced": {"approved"}}, got)
	assert.NotContains(t, got["balanced"], "org-removed-this")
}

// A stale personal pick (e.g. the model left the org list) must fall back to
// the org's list rather than emptying the cluster.
func TestMergeClusterOverrides_EmptyIntersectionFallsBackToKeyList(t *testing.T) {
	got := mergeClusterOverrides(
		map[string][]string{"balanced": {"a", "b"}},
		map[string][]string{"balanced": {"retired-model"}},
	)

	assert.Equal(t, map[string][]string{"balanced": {"a", "b"}}, got)
}

func TestMergeClusterOverrides_EmptyUserListIgnored(t *testing.T) {
	got := mergeClusterOverrides(
		map[string][]string{"balanced": {"a"}},
		map[string][]string{"balanced": {}},
	)

	assert.Equal(t, map[string][]string{"balanced": {"a"}}, got)
}

// An empty key-scoped list for a cluster is not a restriction, so the user's
// selection applies whole.
func TestMergeClusterOverrides_EmptyKeyListLetsUserSelectionThrough(t *testing.T) {
	got := mergeClusterOverrides(
		map[string][]string{"balanced": {}},
		map[string][]string{"balanced": {"a"}},
	)

	assert.Equal(t, map[string][]string{"balanced": {"a"}}, got)
}

// Merging must not mutate either input — both maps are read from shared
// request-scoped state.
func TestMergeClusterOverrides_DoesNotMutateInputs(t *testing.T) {
	key := map[string][]string{"balanced": {"a", "b"}}
	user := map[string][]string{"balanced": {"b"}}

	_ = mergeClusterOverrides(key, user)

	assert.Equal(t, map[string][]string{"balanced": {"a", "b"}}, key)
	assert.Equal(t, map[string][]string{"balanced": {"b"}}, user)
}
