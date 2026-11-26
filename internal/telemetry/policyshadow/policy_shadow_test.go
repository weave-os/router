package policyshadow_test

import (
	"context"

	"testing"
	"time"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/policyshadow"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingShadowStore struct {
	events chan telemetry.PolicyShadowDecision
}

func (s *recordingShadowStore) InsertPolicyShadowDecision(_ context.Context, event telemetry.PolicyShadowDecision) error {
	s.events <- event
	return nil
}

func TestRecorderStampsServingIdentityAndRequestedModel(t *testing.T) {
	store := &recordingShadowStore{events: make(chan telemetry.PolicyShadowDecision, 1)}
	recorder := policyshadow.Recorder{
		Store: store,
		Now:   time.Now,
		Route: func(context.Context, router.Strategy, router.Request) (router.Decision, error) {
			return router.Decision{Model: "shadow-model"}, nil
		},
	}
	recorder.Fire(requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{Target: "prod/stable", ReleaseID: "release-sha"}), "00000000-0000-4000-8000-000000000001", router.StrategyHMMEmbedding, router.StrategyHMMBeta,
		router.Decision{Model: "served-model"}, router.Request{RequestedModel: "weave-auto"})

	select {
	case event := <-store.events:
		assert.Equal(t, "prod/stable", event.ServingTarget)
		assert.Equal(t, "release-sha", event.ServingReleaseID)
		assert.Equal(t, "weave-auto", event.RequestedModel)
		assert.Equal(t, "shadow-model", event.ShadowModel)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "shadow decision was not recorded")
	}
}

func TestRecorderLeavesServingIdentityEmptyOutsideManagedServing(t *testing.T) {
	store := &recordingShadowStore{events: make(chan telemetry.PolicyShadowDecision, 1)}
	recorder := policyshadow.Recorder{
		Store: store,
		Now:   time.Now,
		Route: func(context.Context, router.Strategy, router.Request) (router.Decision, error) {
			return router.Decision{Model: "shadow-model"}, nil
		},
	}

	recorder.Fire(context.Background(), "00000000-0000-4000-8000-000000000001", router.StrategyHMMEmbedding, router.StrategyHMMBeta,
		router.Decision{Model: "served-model"}, router.Request{RequestedModel: "weave-auto"})

	select {
	case event := <-store.events:
		assert.Empty(t, event.ServingTarget)
		assert.Empty(t, event.ServingReleaseID)
		assert.Equal(t, "weave-auto", event.RequestedModel)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "shadow decision was not recorded")
	}
}
