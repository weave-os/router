package proxy

import (
	"context"
	"testing"

	"workweave/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ctxWithAllowedModels(models ...string) context.Context {
	return context.WithValue(context.Background(), InstallationAllowedModelsContextKey{}, models)
}

func ctxWithExcludedAndAllowed(excluded, allowed []string) context.Context {
	ctx := context.Background()
	if len(excluded) > 0 {
		ctx = context.WithValue(ctx, InstallationExcludedModelsContextKey{}, excluded)
	}
	if len(allowed) > 0 {
		ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, allowed)
	}
	return ctx
}

func TestAllowedModelsForRequest_EmptyMeansNoRestriction(t *testing.T) {
	assert.Nil(t, allowedModelsForRequest(context.Background()))
	assert.Nil(t, allowedModelsForRequest(ctxWithAllowedModels()))
}

func TestAllowedModelsForRequest_BuildsSet(t *testing.T) {
	got := allowedModelsForRequest(ctxWithAllowedModels("a", "b"))
	assert.Equal(t, map[string]struct{}{"a": {}, "b": {}}, got)
}

// The allowlist is enforced by desugaring into the exclusion set: every
// routable model absent from a non-empty allowlist must come back excluded.
func TestExcludedModelsForRequest_AllowlistExcludesTheComplement(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{
		"keep-me": {}, "drop-me": {}, "drop-me-too": {},
	}}

	got := s.excludedModelsForRequest(ctxWithAllowedModels("keep-me"))

	require.NotNil(t, got)
	assert.NotContains(t, got, "keep-me")
	assert.Contains(t, got, "drop-me")
	assert.Contains(t, got, "drop-me-too")
}

// Allowlist and exclusions compose: effective set = allowlist minus exclusions.
func TestExcludedModelsForRequest_AllowlistUnionsWithExclusions(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{
		"a": {}, "b": {}, "c": {},
	}}

	got := s.excludedModelsForRequest(ctxWithExcludedAndAllowed([]string{"b"}, []string{"a", "b"}))

	// c is excluded by the allowlist, b by the explicit exclusion, leaving a.
	assert.NotContains(t, got, "a")
	assert.Contains(t, got, "b")
	assert.Contains(t, got, "c")
}

func TestExcludedModelsForRequest_EmptyAllowlistIsANoOp(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{"a": {}, "b": {}}}

	assert.Nil(t, s.excludedModelsForRequest(context.Background()))

	got := s.excludedModelsForRequest(ctxWithExcludedAndAllowed([]string{"a"}, nil))
	assert.Equal(t, map[string]struct{}{"a": {}}, got)
}

// An allowlist entry naming a model this deployment can't serve is a harmless
// no-op rather than an error — a stale entry after a catalog removal must not
// break routing for the models that remain.
func TestExcludedModelsForRequest_UndeployedAllowlistEntryIgnored(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{"a": {}, "b": {}}}

	got := s.excludedModelsForRequest(ctxWithAllowedModels("a", "retired-model"))

	assert.NotContains(t, got, "a")
	assert.Contains(t, got, "b")
	assert.NotContains(t, got, "retired-model")
}

// The env override is a deliberate operator escape hatch: an operator debugging
// a deployment is not silently constrained by one org's allowlist.
func TestExcludedModelsForRequest_EnvOverrideBypassesAllowlist(t *testing.T) {
	override := map[string]struct{}{"env-excluded": {}}
	s := &Service{
		availableModels:        map[string]struct{}{"a": {}, "b": {}},
		excludedModelsOverride: override,
	}

	got := s.excludedModelsForRequest(ctxWithAllowedModels("a"))

	assert.Equal(t, override, got)
	assert.NotContains(t, got, "b")
}

// nil availableModels means "every catalog model is routable". Missing this
// would make the allowlist a silent no-op on such deployments.
func TestRoutableUniverse_NilAvailableModelsFallsBackToCatalog(t *testing.T) {
	s := &Service{}

	universe := s.routableUniverse()

	require.NotEmpty(t, universe)
	assert.Len(t, universe, len(catalog.Models))
	for _, m := range catalog.Models {
		assert.Contains(t, universe, m.ID)
	}
}

func TestExcludedModelsForRequest_AllowlistAppliesWithNilAvailableModels(t *testing.T) {
	s := &Service{}
	require.NotEmpty(t, catalog.Models, "catalog must be non-empty for this test to mean anything")
	keep := catalog.Models[0].ID

	got := s.excludedModelsForRequest(ctxWithAllowedModels(keep))

	assert.NotContains(t, got, keep)
	assert.Len(t, got, len(catalog.Models)-1)
}
