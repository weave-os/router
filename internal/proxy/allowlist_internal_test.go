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

func TestAllowedModelsForRequest_IntersectsSubscriptionConditionalList(t *testing.T) {
	ctx := ctxWithAllowedModels("a", "b")
	ctx = context.WithValue(ctx, InstallationSubscriptionConditionalModelsContextKey{}, []string{"b", "c"})

	assert.Equal(t, map[string]struct{}{"b": {}}, allowedModelsForRequest(ctx))
}

func TestAllowedModelsForRequest_EmptyConditionalIntersectionFailsClosed(t *testing.T) {
	ctx := ctxWithAllowedModels("a")
	ctx = context.WithValue(ctx, InstallationSubscriptionConditionalModelsContextKey{}, []string{"b"})

	assert.NotNil(t, allowedModelsForRequest(ctx))
	assert.Empty(t, allowedModelsForRequest(ctx))
}

func TestAllowedModelsForRequest_EmptyConfiguredConditionalListFailsClosed(t *testing.T) {
	ctx := context.WithValue(context.Background(), InstallationSubscriptionConditionalModelsContextKey{}, []string{})

	assert.True(t, subscriptionConditionalModelsConfigured(ctx))
	assert.NotNil(t, allowedModelsForRequest(ctx))
	assert.Empty(t, allowedModelsForRequest(ctx))
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

// An undeployed allowlist entry (e.g. stale after catalog removal) must
// be a silent no-op, not an error that breaks routing for remaining models.
func TestExcludedModelsForRequest_UndeployedAllowlistEntryIgnored(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{"a": {}, "b": {}}}

	got := s.excludedModelsForRequest(ctxWithAllowedModels("a", "retired-model"))

	assert.NotContains(t, got, "a")
	assert.Contains(t, got, "b")
	assert.NotContains(t, got, "retired-model")
}

// WhollyUnroutableAllowlistEmptiesThePool: no routable entry means the
// desugaring excludes the entire pool, so every routed request 400s.
func TestExcludedModelsForRequest_WhollyUnroutableAllowlistEmptiesThePool(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{"a": {}, "b": {}}}

	got := s.excludedModelsForRequest(ctxWithAllowedModels("not-routable-1", "not-routable-2"))

	assert.Equal(t, s.availableModels, got, "every routable model must be excluded")
}

// RoutableModels must match the desugaring universe exactly.
func TestRoutableModels_MatchesDesugaringUniverse(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{"a": {}, "b": {}}}

	assert.Equal(t, s.routableUniverse(), s.RoutableModels())
}

// The accessor must return a copy so callers cannot mutate routing state.
func TestRoutableModels_ReturnsACopy(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{"a": {}}}

	got := s.RoutableModels()
	got["injected"] = struct{}{}

	assert.NotContains(t, s.availableModels, "injected")
}

// A typed-nil *Service from server.Register must not panic.
func TestRoutableModels_NilServiceReportsUnknownUniverse(t *testing.T) {
	var s *Service

	assert.Nil(t, s.RoutableModels())
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
