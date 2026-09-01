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

// An empty allowlist clears the restriction and must never be blocked.
func TestAllowlistLosesRoutability_EmptyAllowlistAlwaysPasses(t *testing.T) {
	assert.False(t, allowlistLosesRoutability(nil, known("a"), routable("a")))
	assert.False(t, allowlistLosesRoutability([]string{}, known("a"), routable("a")))
}

// One routable survivor is enough; the rest may be force-model-only.
func TestAllowlistLosesRoutability_PartialOverlapPasses(t *testing.T) {
	assert.False(t, allowlistLosesRoutability(
		[]string{"passthrough-only", "a"}, known("passthrough-only", "a"), routable("a", "b")))
}

// A wholly non-routable allowlist would 400 every routed request.
func TestAllowlistLosesRoutability_DisjointAllowlistFails(t *testing.T) {
	assert.True(t, allowlistLosesRoutability(
		[]string{"claude-opus-4-8"}, known("claude-opus-4-8", "claude-opus-4-7"), routable("claude-opus-4-7")))
	assert.True(t, allowlistLosesRoutability(
		[]string{"x", "y"}, known("x", "y", "a"), routable("a", "b")))
}

// Unknown IDs defer to SetInstallationAllowedModels, not this guard.
func TestAllowlistLosesRoutability_UnknownIDDefersToMembershipCheck(t *testing.T) {
	assert.False(t, allowlistLosesRoutability(
		[]string{"typo"}, known("a", "b"), routable("a")))
}

// Unknown universe fails open so a proxy-less router stays editable.
func TestAllowlistLosesRoutability_UnknownUniverseFailsOpen(t *testing.T) {
	assert.False(t, allowlistLosesRoutability([]string{"anything"}, known("anything"), nil))
	assert.False(t, allowlistLosesRoutability([]string{"anything"}, known("anything"), routable()))
}

// Catalog membership is wider than the routable set; otherwise this guard is dead.
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
