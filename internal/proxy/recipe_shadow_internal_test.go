package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/policyshadow"
	"weave-os/router/internal/telemetry/turnrecord"
)

type recipeShadowSpy struct{ fired chan struct{} }

func (s recipeShadowSpy) ServedFacts(router.Decision) (router.RecipeShadowServed, bool) {
	s.fired <- struct{}{}
	return router.RecipeShadowServed{}, false
}

func (recipeShadowSpy) Replay(context.Context, string, router.Decision, router.Request) (router.RecipeShadowPick, error) {
	return router.RecipeShadowPick{}, nil
}

type recipeShadowStoreStub struct{ turnrecord.TelemetryRepository }

func (recipeShadowStoreStub) InsertPolicyShadowDecision(context.Context, telemetry.PolicyShadowDecision) error {
	return nil
}

type recipeProfilesStub struct{}

func (recipeProfilesStub) ProfileKey(context.Context, string, router.RoutingModel) (string, bool, error) {
	return "", false, nil
}

func recipeShadowFired(ctx context.Context) bool {
	spy := recipeShadowSpy{fired: make(chan struct{}, 1)}
	tel := turnTelemetry{repo: recipeShadowStoreStub{}, recipe: policyshadow.RecipeRecorder{
		Profiles: recipeProfilesStub{}, Replayer: spy, ShadowModel: router.ModelAutoEfficient,
	}}
	fireRecipeShadow(ctx, tel, auth.Clock(time.Now), "11111111-1111-4111-8111-111111111111", router.StrategyHMM, router.Decision{}, router.Request{})
	select {
	case <-spy.fired:
		return true
	case <-time.After(50 * time.Millisecond):
		return false
	}
}

func recipeShadowEnabled() context.Context {
	managed := requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{Target: "prod/stable"})
	return flags.WithOverrides(managed, flags.Overrides{Bools: map[flags.Key]bool{flags.KeyRecipeShadow: true}})
}

func TestRecipeShadowIgnoresACallerSuppliedFlagOverlay(t *testing.T) {
	managed := requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{Target: "prod/stable"})
	overlay := flags.Overrides{Bools: map[flags.Key]bool{flags.KeyRecipeShadow: true}}

	callerEnabled := flags.WithRequestOverlay(managed, flags.Overrides{}, overlay)
	assert.False(t, recipeShadowFired(callerEnabled), "a caller's x-weave-flag-overrides overlay cannot enrol an installation the operator did not")

	operatorEnabled := flags.WithRequestOverlay(managed, overlay, flags.Overrides{})
	assert.True(t, recipeShadowFired(operatorEnabled), "the operator's stored override still enables the shadow")
}

func TestRecipeShadowIsOffUnlessFlagged(t *testing.T) {
	assert.False(t, recipeShadowFired(requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{Target: "prod/stable"})))
}

func TestRecipeShadowSkipsTurnsWithoutAServingIdentity(t *testing.T) {
	unmanaged := flags.WithOverrides(context.Background(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyRecipeShadow: true}})
	assert.False(t, recipeShadowFired(unmanaged), "without a serving identity the serving profile is unknown, so nothing is compared")
}

func TestRecipeShadowObservesOnlyDefaultRecipeTurns(t *testing.T) {
	enabled := recipeShadowEnabled()
	for model, want := range map[router.RoutingModel]bool{
		"":                        true,
		router.ModelAutomatic:     true,
		router.ModelAutoEfficient: false,
		router.ModelHMMBeta:       false,
	} {
		ctx := router.WithStrategySelection(enabled, router.StrategySelection{Strategy: router.StrategyHMM, RoutingModel: model})
		assert.Equal(t, want, recipeShadowFired(ctx), "routing model %q", model)
	}
}
