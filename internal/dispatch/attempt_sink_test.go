package dispatch_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
)

type credentialTestKey struct{}

type heldAttemptStore struct {
	started chan context.Context
	release chan struct{}
	events  chan proxy.InsertInferenceAttemptParams
}

func (s *heldAttemptStore) InsertInferenceAttempt(ctx context.Context, p proxy.InsertInferenceAttemptParams) error {
	s.started <- ctx
	select {
	case <-s.release:
		s.events <- p
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestHeldAttemptPersistenceDoesNotDelayNextAllowedAttempt(t *testing.T) {
	workers := observability.NewObservationWorkers()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = workers.Shutdown(ctx)
	})
	store := &heldAttemptStore{started: make(chan context.Context, 2), release: make(chan struct{}), events: make(chan proxy.InsertInferenceAttemptParams, 2)}
	t.Cleanup(func() { close(store.release) })
	executor, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{
		providers.ProviderFireworks:  &fakeUpstream{errs: []error{&providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable}}},
		providers.ProviderOpenRouter: &fakeUpstream{},
	}), dispatch.WithAttemptSink(proxy.NewAttemptSink(store, workers.Database)), dispatch.WithSleep(func(context.Context, time.Duration) error { return nil }))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, "installation-test"))
	ctx = context.WithValue(ctx, credentialTestKey{}, "must-not-retain")
	defer cancel()
	completed := make(chan dispatch.Result, 1)
	failures := make(chan error, 1)
	go func() {
		result, err := executor.Run(ctx, inference.InvocationRequest{RequestID: "request-test"}, fakePlan{selected: primary, alternatives: []inference.Target{backup}}, dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`)), OperationID: "operation-test"})
		failures <- err
		completed <- result
	}()
	select {
	case result := <-completed:
		require.NoError(t, <-failures)
		assert.Equal(t, backup, result.Outcome.ServedTarget)
		assert.Equal(t, 2, result.Outcome.AttemptCount)
	case <-time.After(time.Second):
		t.Fatal("attempt persistence blocked provider failover")
	}
	var workerCtx context.Context
	select {
	case workerCtx = <-store.started:
	case <-time.After(time.Second):
		t.Fatal("attempt was not submitted")
	}
	cancel()
	assert.NoError(t, workerCtx.Err())
	assert.Nil(t, workerCtx.Value(credentialTestKey{}))
	deadline, ok := workerCtx.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(5*time.Second), deadline, time.Second)
	// Allow admitted writes, then verify the IDs/index survive request cancellation.
	store.release <- struct{}{}
	store.release <- struct{}{}
	first, second := <-store.events, <-store.events
	assert.WithinDuration(t, time.Now(), first.Timestamp, time.Second)
 assert.Equal(t, "installation-test", first.InstallationID)
	assert.Equal(t, "request-test", first.Event.RequestID)
	assert.Equal(t, "operation-test", second.Event.OperationID)
	assert.Equal(t, 0, first.Event.AttemptIndex)
	assert.Equal(t, 1, second.Event.AttemptIndex)
	assert.Equal(t, inference.AttemptOutcomeFailed, first.Event.Outcome)
	assert.Equal(t, inference.AttemptOutcomeServed, second.Event.Outcome)
	assert.Equal(t, backup, second.Event.Target)
}
