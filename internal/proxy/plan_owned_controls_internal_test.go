package proxy

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func planOwnedContext() context.Context {
	alpha := 0.9
	ctx := requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{Plan: "max"})
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

func TestPlanOwnedServingIgnoresForceModelHeader(t *testing.T) {
	t.Parallel()

	request, err := http.NewRequestWithContext(planOwnedContext(), http.MethodPost, "http://router.test/v1/messages", nil)
	require.NoError(t, err)
	request.Header.Set(ForceModelHeader, "claude-opus-5")

	ctx, forced, err := (&Service{}).applyForceModelHeader(request.Context(), request, uuid.Nil, [sessionpin.SessionKeyLen]byte{})
	require.NoError(t, err)
	assert.Empty(t, forced)
	assert.True(t, planOwnedServingRequest(ctx))
}

func TestPlanOwnedServingIgnoresLegacyForceModelPin(t *testing.T) {
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

	pin, active, noStoredState := svc.loadPinWithStoreState(planOwnedContext(), sessionKey, role)

	assert.Equal(t, "claude-opus-5", pin.Model)
	assert.False(t, active)
	assert.False(t, noStoredState)
}
