package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/subscriptions"
)

func planAwareUniverse() map[string]struct{} {
	return map[string]struct{}{
		"claude-sonnet-5":  {},
		"claude-opus-4-8":  {},
		"gpt-5.6-sol":      {},
		"gemini-3.8-flash": {},
	}
}

func planAwareStates(states map[subscriptions.Provider]SubscriptionPlanState) context.Context {
	return context.WithValue(context.Background(), ManagedSubscriptionPlanStatesContextKey{}, states)
}

func TestManagedSubscriptionPlanStatesAggregatesAccountCooldowns(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(time.Hour)

	states := ManagedSubscriptionPlanStates([]*auth.SubscriptionAccount{
		{
			Provider: auth.SubscriptionProviderClaude,
			Enabled:  true,
		},
		{
			Provider:      auth.SubscriptionProviderCodex,
			Enabled:       true,
			CooldownUntil: &resetAt,
		},
	}, now)

	assert.Equal(t, SubscriptionPlanStateActive, states[subscriptions.ProviderClaude])
	assert.Equal(t, SubscriptionPlanStateExhausted, states[subscriptions.ProviderCodex])
}

func TestPlanAwareRoutingLeavesRosterUnchangedWhenPlansAreActive(t *testing.T) {
	svc := &Service{
		availableModels:              planAwareUniverse(),
		planAwareSubscriptionRouting: true,
	}
	ctx := svc.withPlanAwareSubscriptionModels(planAwareStates(map[subscriptions.Provider]SubscriptionPlanState{
		subscriptions.ProviderClaude: SubscriptionPlanStateActive,
		subscriptions.ProviderCodex:  SubscriptionPlanStateActive,
	}), nil)

	assert.Nil(t, subscriptionPlanAwareExcludedModelsFromContext(ctx))
	assert.Nil(t, svc.excludedModelsForRequest(ctx))
}

func TestPlanAwareRoutingExcludesOnlyExhaustedPlanModels(t *testing.T) {
	svc := &Service{
		availableModels:              planAwareUniverse(),
		planAwareSubscriptionRouting: true,
	}
	ctx := svc.withPlanAwareSubscriptionModels(planAwareStates(map[subscriptions.Provider]SubscriptionPlanState{
		subscriptions.ProviderClaude: SubscriptionPlanStateExhausted,
		subscriptions.ProviderCodex:  SubscriptionPlanStateActive,
	}), nil)

	excluded := svc.excludedModelsForRequest(ctx)

	assert.Contains(t, excluded, "claude-sonnet-5")
	assert.Contains(t, excluded, "claude-opus-4-8")
	assert.NotContains(t, excluded, "gpt-5.6-sol")
	assert.NotContains(t, excluded, "gemini-3.8-flash")
}

func TestPlanAwareRoutingRestoresNormalRosterWhenAllPlansAreExhausted(t *testing.T) {
	svc := &Service{
		availableModels:              planAwareUniverse(),
		planAwareSubscriptionRouting: true,
	}
	ctx := svc.withPlanAwareSubscriptionModels(planAwareStates(map[subscriptions.Provider]SubscriptionPlanState{
		subscriptions.ProviderClaude: SubscriptionPlanStateExhausted,
		subscriptions.ProviderCodex:  SubscriptionPlanStateExhausted,
	}), nil)

	assert.Nil(t, subscriptionPlanAwareExcludedModelsFromContext(ctx))
	assert.Nil(t, svc.excludedModelsForRequest(ctx))
}

func TestPlanAwareRoutingDoesNotFilterOnUnknownPlanState(t *testing.T) {
	svc := &Service{
		availableModels:              planAwareUniverse(),
		planAwareSubscriptionRouting: true,
	}
	ctx := svc.withPlanAwareSubscriptionModels(planAwareStates(map[subscriptions.Provider]SubscriptionPlanState{
		subscriptions.ProviderClaude: SubscriptionPlanStateUnknown,
		subscriptions.ProviderCodex:  SubscriptionPlanStateActive,
	}), nil)

	require.Nil(t, subscriptionPlanAwareExcludedModelsFromContext(ctx))
	assert.Nil(t, svc.excludedModelsForRequest(ctx))
}

func TestPlanAwareRoutingComposesWithExplicitExclusions(t *testing.T) {
	svc := &Service{
		availableModels:              planAwareUniverse(),
		planAwareSubscriptionRouting: true,
	}
	ctx := svc.withPlanAwareSubscriptionModels(planAwareStates(map[subscriptions.Provider]SubscriptionPlanState{
		subscriptions.ProviderClaude: SubscriptionPlanStateExhausted,
		subscriptions.ProviderCodex:  SubscriptionPlanStateActive,
	}), nil)
	ctx = context.WithValue(ctx, InstallationExcludedModelsContextKey{}, []string{"gpt-5.6-sol"})

	excluded := svc.excludedModelsForRequest(ctx)

	assert.Contains(t, excluded, "claude-sonnet-5")
	assert.Contains(t, excluded, "gpt-5.6-sol")
	assert.NotContains(t, excluded, "gemini-3.8-flash")
}
