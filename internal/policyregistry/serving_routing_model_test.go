package policyregistry

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions/entitlement"
)

// routingAdmissionStore serializes callbacks just like the primary conversation
// lock; caches are independent workers sharing this durable binding.
type routingAdmissionStore struct {
	mu         sync.Mutex
	projection AdmissionProjection
	bindings   map[string]SessionReleaseBinding
	calls      int
}

func (s *routingAdmissionStore) Admit(ctx context.Context, installation, key, session string, decide AdmissionDecision) (AdmissionScope, SessionReleaseBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	scope := AdmissionScope{InstallationID: installation, CredentialIdentity: key, Persistent: session != ""}
	var previous *SessionReleaseBinding
	if stored, found := s.bindings[session]; found && scope.Persistent {
		previous = &stored
	}
	binding, err := decide(ctx, SerializedAdmission{Projection: s.projection, Previous: previous})
	if err == nil && scope.Persistent {
		if s.bindings == nil {
			s.bindings = make(map[string]SessionReleaseBinding)
		}
		s.bindings[session] = binding
	}
	return scope, binding, err
}

func routingReleaseDecision(_ context.Context, admission SerializedAdmission) (SessionReleaseBinding, error) {
	binding := SessionReleaseBinding{Target: admission.Projection.Target, Plan: admission.Projection.Plan, BindingGeneration: 1}
	if admission.Previous != nil {
		binding = *admission.Previous
	}
	return binding, nil
}

func routingAdmissionCache(t *testing.T, store ServingAdmissionStore) *AdmissionDecisionCache {
	t.Helper()
	cache, err := NewAdmissionDecisionCache(store, 10, time.Minute, time.Now)
	require.NoError(t, err)
	return cache
}

func TestRoutingModelAdmissionRejectsConversationChangesAcrossWorkers(t *testing.T) {
	store := &routingAdmissionStore{projection: AdmissionProjection{Target: TargetInternal}}
	first, second := routingAdmissionCache(t, store), routingAdmissionCache(t, store)
	_, binding, err := first.AdmitRoutingModel(context.Background(), "installation", "credential", "conversation", router.ModelAutomatic, routingReleaseDecision)
	require.NoError(t, err)
	assert.Equal(t, router.ModelAutomatic, binding.RoutingModel)
	for _, cache := range []*AdmissionDecisionCache{first, second} {
		for _, model := range []router.RoutingModel{router.ModelHMM, router.ModelQwen} {
			_, _, err := cache.AdmitRoutingModel(context.Background(), "installation", "credential", "conversation", model, routingReleaseDecision)
			require.ErrorIs(t, err, ErrRoutingModelChanged)
		}
		_, retained, err := cache.AdmitRoutingModel(context.Background(), "installation", "credential", "conversation", router.ModelAutomatic, routingReleaseDecision)
		require.NoError(t, err)
		assert.Equal(t, router.ModelAutomatic, retained.RoutingModel)
	}
	_, independent, err := second.AdmitRoutingModel(context.Background(), "installation", "credential", "new-conversation", router.ModelQwen, routingReleaseDecision)
	require.NoError(t, err)
	assert.Equal(t, router.ModelQwen, independent.RoutingModel)
}

func TestRoutingModelAdmissionConcurrentSelectorsHaveOneDurableWinner(t *testing.T) {
	store := &routingAdmissionStore{projection: AdmissionProjection{Target: TargetInternal}}
	first, second := routingAdmissionCache(t, store), routingAdmissionCache(t, store)
	type outcome struct {
		binding SessionReleaseBinding
		err     error
	}
	outcomes := make(chan outcome, 3)
	start := make(chan struct{})
	for index, model := range []router.RoutingModel{router.ModelAutomatic, router.ModelHMM, router.ModelQwen} {
		cache := []*AdmissionDecisionCache{first, second}[index%2]
		go func() {
			<-start
			_, binding, err := cache.AdmitRoutingModel(context.Background(), "installation", "credential", "conversation", model, routingReleaseDecision)
			outcomes <- outcome{binding: binding, err: err}
		}()
	}
	close(start)
	var winner router.RoutingModel
	successes := 0
	for range 3 {
		completed := <-outcomes
		if completed.err != nil {
			require.ErrorIs(t, completed.err, ErrRoutingModelChanged)
			continue
		}
		successes++
		winner = completed.binding.RoutingModel
	}
	require.Equal(t, 1, successes)
	// A cold worker must read the committed winner rather than choose again.
	cold := routingAdmissionCache(t, store)
	_, binding, err := cold.AdmitRoutingModel(context.Background(), "installation", "credential", "conversation", winner, routingReleaseDecision)
	require.NoError(t, err)
	assert.Equal(t, winner, binding.RoutingModel)
}

func TestRoutingModelAdmissionUpgradesLegacyCachedBinding(t *testing.T) {
	store := &routingAdmissionStore{projection: AdmissionProjection{Target: TargetInternal}}
	cache := routingAdmissionCache(t, store)
	_, legacy, err := cache.Admit(context.Background(), "installation", "credential", "conversation", routingReleaseDecision)
	require.NoError(t, err)
	require.Empty(t, legacy.RoutingModel)
	_, bound, err := cache.AdmitRoutingModel(context.Background(), "installation", "credential", "conversation", router.ModelHMM, routingReleaseDecision)
	require.NoError(t, err)
	assert.Equal(t, router.ModelHMM, bound.RoutingModel)
	assert.Equal(t, 2, store.calls, "legacy cache cannot bypass the durable first selection")
	_, _, err = cache.AdmitRoutingModel(context.Background(), "installation", "credential", "conversation", router.ModelQwen, routingReleaseDecision)
	require.ErrorIs(t, err, ErrRoutingModelChanged)
}

func TestRoutingModelAdmissionSessionRequirementAndSubscriberIsolation(t *testing.T) {
	store := &routingAdmissionStore{projection: AdmissionProjection{Target: TargetInternal}}
	cache := routingAdmissionCache(t, store)
	for _, model := range []router.RoutingModel{router.ModelHMM, router.ModelQwen} {
		_, _, err := cache.AdmitRoutingModel(context.Background(), "installation", "credential", "", model, routingReleaseDecision)
		require.ErrorIs(t, err, ErrRoutingModelSessionRequired)
	}
	_, automatic, err := cache.AdmitRoutingModel(context.Background(), "installation", "credential", "", router.ModelAutomatic, routingReleaseDecision)
	require.NoError(t, err)
	assert.Empty(t, automatic.RoutingModel)
	store.projection = AdmissionProjection{Target: TargetStable, Plan: entitlement.PlanMax}
	for _, model := range []router.RoutingModel{router.ModelAutomatic, router.ModelQwen} {
		_, subscriber, err := cache.AdmitRoutingModel(context.Background(), "installation", "credential", "subscriber-conversation", model, routingReleaseDecision)
		require.NoError(t, err)
		assert.Empty(t, subscriber.RoutingModel, "subscriber routing remains governed by its plan")
	}
}
