package proxy

import (
	"context"
	"testing"
	"time"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A pin with an unavailable provider falls through to automatic routing (#874).
func TestRunTurnLoop_ForcedPin_FallsThroughWhenProviderUnavailable(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{
		"deepseek/deepseek-v4-flash": {},
		"claude-sonnet-4-6":          {},
	}}
	store := &forcedPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderMakora,
		Model:       "deepseek/deepseek-v4-flash",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: time.Now().Add(time.Hour),
	}}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithAvailableModels(fr.available).
		WithPlannerEnabled(false)

	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)

	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil, router.Request{
		RequestedModel: feats.Model,
		// Makora is excluded from this turn's enabled set.
		EnabledProviders: map[string]struct{}{providers.ProviderOpenRouter: {}, providers.ProviderAnthropic: {}},
	})
	require.NoError(t, err)

	assert.False(t, res.StickyHit, "a pin on an unavailable provider must not be served as a sticky hit")
	assert.NotEqual(t, providers.ProviderMakora, res.Decision.Provider,
		"routing must not serve the excluded provider")
}
