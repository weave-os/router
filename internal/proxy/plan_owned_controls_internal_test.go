package proxy

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func planOwnedContext() context.Context {
	return planOwnedContextFor(entitlement.PlanMax)
}

func boostPlanOwnedContext() context.Context {
	return planOwnedContextFor(entitlement.PlanBoost)
}

func planOwnedContextFor(plan entitlement.Plan) context.Context {
	alpha := 0.9
	ctx := requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{Plan: string(plan)})
	ctx = entitlement.WithProductScope(ctx, plan)
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{"customer-allowed"})
	ctx = context.WithValue(ctx, InstallationExcludedModelsContextKey{}, []string{"customer-excluded"})
	ctx = context.WithValue(ctx, InstallationExcludedProvidersContextKey{}, []string{"customer-provider"})
	ctx = context.WithValue(ctx, InstallationPreferredModelsContextKey{}, []string{"customer-preferred"})
	ctx = context.WithValue(ctx, InstallationFastModeModelsContextKey{}, []string{"customer-fast"})
	ctx = context.WithValue(ctx, SubscriptionStatePreferredModelsContextKey{}, []string{"customer-subscription-preferred"})
	ctx = context.WithValue(ctx, ClusterModelListsContextKey{}, map[string][]string{"cluster": {"customer-arm"}})
	return router.WithRoutingKnobs(ctx, &router.Overrides{Alpha: &alpha})
}

func TestPlanOwnedServingIgnoresCustomerRoutingControls(t *testing.T) {
	t.Parallel()

	ctx := planOwnedContext()
	assert.Nil(t, allowedModelsForRequest(ctx))
	assert.Nil(t, installationAllowedModelSet(ctx))
	assert.Nil(t, installationExcludedProvidersFromContext(ctx))
	assert.Nil(t, installationFastModeModelsFromContext(ctx))
	assert.Nil(t, routingKnobsForRequest(ctx))
	assert.Nil(t, (&Service{}).preferredModelsForRequest(ctx))
	assert.Nil(t, subscriptionStatePreferredModelsFromContext(ctx))
	assert.Nil(t, clusterArmOverridesForRequest(ctx))
	assert.NotContains(t, (&Service{}).excludedModelsForRequest(ctx), "customer-excluded")
}

func TestBoostServingAllowsForceModelHeader(t *testing.T) {
	t.Parallel()

	request, err := http.NewRequestWithContext(boostPlanOwnedContext(), http.MethodPost, "http://router.test/v1/messages", nil)
	require.NoError(t, err)
	request.Header.Set(ForceModelHeader, "claude-opus-5")

	ctx, forced, err := (&Service{}).applyForceModelHeader(request.Context(), request, uuid.Nil, [sessionpin.SessionKeyLen]byte{})
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5", forced)
	assert.True(t, planOwnedServingRequest(ctx))
}

func TestBoostServingHeaderForceModelCarriesEffortSameTurn(t *testing.T) {
	t.Parallel()

	request, err := http.NewRequestWithContext(boostPlanOwnedContext(), http.MethodPost, "http://router.test/v1/messages", nil)
	require.NoError(t, err)
	request.Header.Set(ForceModelHeader, "sol:medium")

	svc := &Service{}
	ctx, forced, err := svc.applyForceModelHeader(request.Context(), request, uuid.Nil, [sessionpin.SessionKeyLen]byte{})
	require.NoError(t, err)
	assert.Equal(t, "gpt-6.1-sol:medium", forced)

	env := forceCommandEnv(t)
	features := env.RoutingFeatures(false)
	result, err := svc.runTurnLoop(
		ctx, env, features, "api-key", uuid.New(), "", request.Header,
		router.Request{RequestedModel: features.Model, ForceModel: forced},
	)
	require.NoError(t, err)
	assert.Equal(t, "gpt-6.1-sol", result.Decision.Model)
	assert.Equal(t, "medium", result.Decision.Effort)
}

func TestBoostServingKeepsLegacyForceModelPinActive(t *testing.T) {
	t.Parallel()

	sessionKey := [sessionpin.SessionKeyLen]byte{1}
	role := sessionpin.DefaultRole
	store := newForceModelMapStore()
	store.pins[forceModelMapKey(sessionKey, role)] = sessionpin.Pin{
		SessionKey:  sessionKey,
		Role:        role,
		Model:       "claude-opus-5",
		Provider:    providers.ProviderAnthropic,
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: pinNeverExpires,
	}
	svc := &Service{pinStore: store}

	pin, active, noStoredState := svc.loadPinWithStoreState(boostPlanOwnedContext(), sessionKey, role)

	assert.Equal(t, "claude-opus-5", pin.Model)
	assert.True(t, active)
	assert.False(t, noStoredState)
}

func TestBoostServingAppliesForceModelCommand(t *testing.T) {
	store := newForceModelMapStore()
	svc := &Service{pinStore: store}
	env := forceCommandEnv(t)
	threadKey := [sessionpin.SessionKeyLen]byte{2}
	forceKey := [sessionpin.SessionKeyLen]byte{3}

	forcedModel, message, err := svc.applyForceModelCommand(
		boostPlanOwnedContext(), env, translate.ForceModelResult{Model: "sol"}, uuid.New(), threadKey, forceKey,
	)
	require.NoError(t, err)
	assert.Equal(t, "gpt-6.1-sol", forcedModel)
	assert.Contains(t, message, "force-model applied")
	assert.NotContains(t, message, "automatic model selection")

	stored, found, err := store.Get(context.Background(), forceKey, forceModelSessionRole)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "gpt-6.1-sol", stored.Model)
}

func TestBoostServingRoutesExplicitForceModel(t *testing.T) {
	env := forceCommandEnv(t)
	features := env.RoutingFeatures(false)
	svc := &Service{}

	result, err := svc.runTurnLoop(
		boostPlanOwnedContext(), env, features, "api-key", uuid.New(), "", nil,
		router.Request{RequestedModel: features.Model, ForceModel: "sol"},
	)
	require.NoError(t, err)
	assert.Equal(t, "gpt-6.1-sol", result.Decision.Model)
	assert.Equal(t, translate.ReasonUserForceModel, result.Decision.Reason)
}

func TestMaxServingStillRejectsClosedSourceForceModel(t *testing.T) {
	store := newForceModelMapStore()
	svc := &Service{pinStore: store}
	env := forceCommandEnv(t)
	_, message, err := svc.applyForceModelCommand(
		planOwnedContext(), env, translate.ForceModelResult{Model: "opus"}, uuid.New(),
		[sessionpin.SessionKeyLen]byte{4}, [sessionpin.SessionKeyLen]byte{5},
	)
	require.NoError(t, err)
	assert.Contains(t, message, "force-model rejected")
	assert.Empty(t, store.pins)
}
