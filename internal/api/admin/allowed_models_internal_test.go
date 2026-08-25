package admin

import (
	"testing"

	"workweave/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRoutableModels struct {
	models map[string]struct{}
}

func (f fakeRoutableModels) RoutableModels() map[string]struct{} { return f.models }

func routable(ids ...string) fakeRoutableModels {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return fakeRoutableModels{models: out}
}

// known builds the catalog-membership set the guard checks IDs against.
func known(ids ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

// Clearing the restriction must never be blocked — an org that has locked
// itself out needs the empty list to keep working.
func TestAllowlistLosesRoutability_EmptyAllowlistAlwaysPasses(t *testing.T) {
	assert.False(t, allowlistLosesRoutability(nil, known("a"), routable("a")))
	assert.False(t, allowlistLosesRoutability([]string{}, known("a"), routable("a")))
}

// One routable survivor is enough; the rest may be force-model-only rows.
func TestAllowlistLosesRoutability_PartialOverlapPasses(t *testing.T) {
	assert.False(t, allowlistLosesRoutability(
		[]string{"passthrough-only", "a"}, known("passthrough-only", "a"), routable("a", "b")))
}

// The regression this guard exists for: a catalog-valid but wholly non-routable
// allowlist desugars into "exclude every routable model", so every routed
// request 400s with ErrAllowlistEmptiesPool until an admin widens the list.
func TestAllowlistLosesRoutability_DisjointAllowlistFails(t *testing.T) {
	assert.True(t, allowlistLosesRoutability(
		[]string{"claude-opus-4-8"}, known("claude-opus-4-8", "claude-opus-4-7"), routable("claude-opus-4-7")))
	assert.True(t, allowlistLosesRoutability(
		[]string{"x", "y"}, known("x", "y", "a"), routable("a", "b")))
}

// An unknown ID belongs to SetInstallationAllowedModels' error, not this one.
func TestAllowlistLosesRoutability_UnknownIDDefersToMembershipCheck(t *testing.T) {
	assert.False(t, allowlistLosesRoutability(
		[]string{"typo"}, known("a", "b"), routable("a")))
}

// Fail open when the deployment's routable set is unknown: a router wired
// without a proxy must still be able to edit its allowlist.
func TestAllowlistLosesRoutability_UnknownUniverseFailsOpen(t *testing.T) {
	assert.False(t, allowlistLosesRoutability([]string{"anything"}, known("anything"), nil))
	assert.False(t, allowlistLosesRoutability([]string{"anything"}, known("anything"), routable()))
}

// The two universes are deliberately different sizes: membership validation is
// catalog-wide, the guard is routable-only. If they ever coincided the guard
// would be dead code, so assert the gap is real.
func TestFullCatalogExceedsRoutableUniverse(t *testing.T) {
	catalogIDs := fullCatalogDTO()
	require.NotEmpty(t, catalogIDs)

	// Every provider bound: still narrower than the catalog, because
	// passthrough-only rows carry no tier and are never scored.
	all := make(map[string]struct{})
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			all[b.Provider] = struct{}{}
		}
	}
	targets := catalog.RoutingTargetSet(all)

	require.NotEmpty(t, targets)
	assert.Less(t, len(targets), len(catalogIDs),
		"expected catalog rows that no deployment can route; guard would be dead code otherwise")
}
