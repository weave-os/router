package router_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
)

func TestHigherEffortPrefersRecognizedLevels(t *testing.T) {
	assert.Equal(t, "low", router.HigherEffort("garbage", "fast"), "an unrecognized level loses to a recognized one")
	assert.Equal(t, "low", router.HigherEffort("min", "garbage"))
	assert.Equal(t, "xhigh", router.HigherEffort("max", "ultra"))
	assert.Equal(t, "high", router.HigherEffort("high", "medium"))
}

func TestAllowlistEmptiesPoolStillMatchesNoEligibleProvider(t *testing.T) {
	assert.ErrorIs(t, router.ErrAllowlistEmptiesPool, router.ErrNoEligibleProvider)
}

func TestStrategyContextDefaultsToHMMEmbedding(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, router.StrategyHMMEmbedding, router.StrategyFromContext(ctx))
	assert.Equal(t, ctx, router.WithStrategy(ctx, ""), "an empty strategy leaves ctx unchanged")
	assert.Equal(t, router.Strategy("rl"), router.StrategyFromContext(router.WithStrategy(ctx, router.Strategy("rl"))))
}

func TestRoutingModelVariantsFallBackToPrimary(t *testing.T) {
	primary, ok := router.RoutingModelPolicyFor(router.ModelHMM)
	require.True(t, ok)
	assert.Equal(t, router.PrimaryRoutingStrategy, primary.Strategy)
	assert.False(t, primary.FallsBackToPrimary)
	for _, model := range []router.RoutingModel{router.ModelHMMBeta, router.ModelQwen, router.ModelDecision} {
		policy, ok := router.RoutingModelPolicyFor(model)
		require.True(t, ok, model)
		assert.True(t, policy.FallsBackToPrimary, model)
	}
	_, ok = router.RoutingModelPolicyFor(router.ModelAutomatic)
	assert.False(t, ok)
	assert.True(t, router.IsRoutingModel(string(router.ModelAutomatic)))
}

func TestServedIdentityIncludesEffort(t *testing.T) {
	assert.Equal(t, "m", router.Decision{Model: "m"}.ServedIdentity())
	assert.Equal(t, "m:high", router.Decision{Model: "m", Effort: "high"}.ServedIdentity())
}

func TestRoutingKnobsContextIgnoresNil(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, ctx, router.WithRoutingKnobs(ctx, nil))
	assert.Nil(t, router.RoutingKnobsFromContext(ctx))
}

func TestModelCapabilityQuirks(t *testing.T) {
	supports := map[string][]router.ModelCapability{
		"claude-opus-4-7":   {router.CapAdaptiveThinking, router.CapExtendedContext, router.CapXhighEffort},
		"claude-opus-5":     {router.CapXhighEffort, router.CapServerSideFallback},
		"claude-fable-5-1":  {router.CapAutoToolChoiceOnly, router.CapSamplingParamsUnsupported},
		"claude-sonnet-5-5": {router.CapXhighEffort, router.CapAutoToolChoiceOnly},
		"claude-haiku-4-5":  {router.CapExtendedThinking, router.CapAssistantPrefill},
		"gpt-5.6-sol":       {router.CapXhighEffort},
		"grok-4.6":          {router.CapXhighEffort},
	}
	lacks := map[string][]router.ModelCapability{
		"claude-sonnet-4-6": {router.CapXhighEffort, router.CapAssistantPrefill},
		"claude-sonnet-5":   {router.CapXhighEffort},
		"claude-opus-5":     {router.CapAutoToolChoiceOnly},
		"claude-haiku-4-5":  {router.CapAdaptiveThinking},
		"grok-4.5":          {router.CapXhighEffort},
		"unknown-model":     {router.CapAssistantPrefill},
	}
	for model, caps := range supports {
		for _, capability := range caps {
			assert.True(t, router.Lookup(model).Supports(capability), "%s should support %s", model, capability)
		}
	}
	for model, caps := range lacks {
		for _, capability := range caps {
			assert.False(t, router.Lookup(model).Supports(capability), "%s should not support %s", model, capability)
		}
	}
	assert.NotContains(t, router.Lookup("gpt-5.6-sol").Reasoning().Levels, "max", "max is Codex-backend only")
	for _, model := range []string{"gpt-6-astra", "muse-spark-1.3", "minimax/minimax-m3.1-flash"} {
		assert.True(t, router.Lookup(model).Reasoning().AlwaysOn, model)
	}
	assert.False(t, router.Lookup("gpt-6-sol").Reasoning().AlwaysOn, "Sol accepts reasoning.effort=none")
	require.NoError(t, router.ValidateCatalogReasoningCapabilities())
}
