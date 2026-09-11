package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

type attemptStoreFunc func(context.Context, InsertInferenceAttemptParams) error

func (f attemptStoreFunc) InsertInferenceAttempt(ctx context.Context, p InsertInferenceAttemptParams) error {
	return f(ctx, p)
}

const attemptInstallation = "00000000-0000-0000-0000-000000000001"

func attemptContext() context.Context {
	return context.WithValue(context.Background(), InstallationIDContextKey{}, attemptInstallation)
}

func attemptTestLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestAttemptSinkSaturationPreservesAcceptedOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		var saved []InsertInferenceAttemptParams
		store := attemptStoreFunc(func(ctx context.Context, p InsertInferenceAttemptParams) error {
			if p.Event.AttemptIndex == 0 {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			saved = append(saved, p)
			return nil
		})
		sink := newAttemptSink(store, attemptTestLog(), 1, time.Second)
		ctx, cancel := context.WithCancel(attemptContext())
		cancel()
		event := inference.AttemptEvent{OperationID: "operation-a", AttemptIndex: 0, Outcome: inference.AttemptOutcomeFailed}
		sink.RecordAttempt(ctx, event)
		<-started
		event.AttemptIndex, event.Outcome = 1, inference.AttemptOutcomeServed
		sink.RecordAttempt(ctx, event)
		event.AttemptIndex = 2
		sink.RecordAttempt(ctx, event)
		assert.Equal(t, uint64(1), sink.Dropped())
		close(release)
		require.NoError(t, sink.Shutdown(context.Background()))
		require.Len(t, saved, 2)
		assert.Equal(t, attemptInstallation, saved[0].InstallationID)
		assert.Equal(t, "operation-a", saved[0].Event.OperationID)
		assert.Equal(t, 0, saved[0].Event.AttemptIndex)
		assert.Equal(t, inference.AttemptOutcomeFailed, saved[0].Event.Outcome)
		assert.Equal(t, 1, saved[1].Event.AttemptIndex)
		assert.Equal(t, inference.AttemptOutcomeServed, saved[1].Event.Outcome)
	})
}

func TestAttemptSinkWriteDeadlineAllowsRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var saved []string
		var expired error
		type secretKey struct{}
		var retainedSecret any
		store := attemptStoreFunc(func(ctx context.Context, p InsertInferenceAttemptParams) error {
			retainedSecret = ctx.Value(secretKey{})
			if p.Event.OperationID == "unavailable" {
				<-ctx.Done()
				expired = ctx.Err()
				return expired
			}
			saved = append(saved, p.Event.OperationID)
			return nil
		})
		sink := NewAttemptSink(store, attemptTestLog())
		ctx := context.WithValue(attemptContext(), secretKey{}, "not-for-the-worker")
		sink.RecordAttempt(ctx, inference.AttemptEvent{OperationID: "unavailable"})
		sink.RecordAttempt(ctx, inference.AttemptEvent{OperationID: "recovered"})
		time.Sleep(300 * time.Millisecond)
		require.NoError(t, sink.Shutdown(context.Background()))
		assert.ErrorIs(t, expired, context.DeadlineExceeded)
		assert.Equal(t, []string{"recovered"}, saved)
		assert.Nil(t, retainedSecret)
		assert.Equal(t, uint64(1), sink.Dropped())
	})
}

func TestAttemptSinkShutdownCancelsActiveWriteAndDiscardsQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		var writeErr error
		var writes int
		sink := NewAttemptSink(attemptStoreFunc(func(ctx context.Context, _ InsertInferenceAttemptParams) error {
			writes++
			close(started)
			<-ctx.Done()
			writeErr = ctx.Err()
			return writeErr
		}), attemptTestLog())
		sink.RecordAttempt(attemptContext(), inference.AttemptEvent{OperationID: "active"})
		<-started
		sink.RecordAttempt(attemptContext(), inference.AttemptEvent{OperationID: "pending"})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.ErrorIs(t, sink.Shutdown(ctx), context.Canceled)
		require.NoError(t, sink.Shutdown(context.Background()))
		sink.RecordAttempt(attemptContext(), inference.AttemptEvent{OperationID: "late"})
		assert.ErrorIs(t, writeErr, context.Canceled)
		assert.Equal(t, 1, writes)
		assert.Equal(t, uint64(3), sink.Dropped())
	})
}

func TestAttemptSinkConcurrentShutdownAccountsForAllEvents(t *testing.T) {
	var persisted atomic.Uint64
	sink := NewAttemptSink(attemptStoreFunc(func(context.Context, InsertInferenceAttemptParams) error {
		persisted.Add(1)
		return nil
	}), attemptTestLog())
	var producers sync.WaitGroup
	for range 100 {
		producers.Go(func() { sink.RecordAttempt(attemptContext(), inference.AttemptEvent{}) })
	}
	require.NoError(t, sink.Shutdown(context.Background()))
	producers.Wait()
	assert.Equal(t, uint64(100), persisted.Load()+sink.Dropped())
}

func TestAttemptSinkSkipsUnattributedEvents(t *testing.T) {
	var persisted atomic.Uint64
	sink := NewAttemptSink(attemptStoreFunc(func(context.Context, InsertInferenceAttemptParams) error {
		persisted.Add(1)
		return nil
	}), attemptTestLog())
	sink.RecordAttempt(context.Background(), inference.AttemptEvent{})
	require.NoError(t, sink.Shutdown(context.Background()))
	assert.Zero(t, persisted.Load())
	assert.Zero(t, sink.Dropped())
}

func TestAttemptSinkDoesNotDelayProviderFailover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		sink := NewAttemptSink(attemptStoreFunc(func(ctx context.Context, _ InsertInferenceAttemptParams) error {
			select {
			case <-started:
			default:
				close(started)
			}
			<-ctx.Done()
			return ctx.Err()
		}), attemptTestLog())
		defer func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_ = sink.Shutdown(ctx)
			_ = sink.Shutdown(context.Background())
		}()
		sink.RecordAttempt(attemptContext(), inference.AttemptEvent{})
		<-started
		primary := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable}}}}
		fallback := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("served")}}}
		executor, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: primary, providers.ProviderOpenRouter: fallback}), dispatch.WithAttemptSink(sink))
		require.NoError(t, err)
		s := (&Service{}).WithInferenceExecutor(executor)
		rec := httptest.NewRecorder()
		done := make(chan error, 1)
		go func() {
			_, err := s.dispatchWithFallback(attemptContext(), plannedInputs(rec, newPreludeBuffer(rec), []catalog.ProviderBinding{{Provider: providers.ProviderFireworks}, {Provider: providers.ProviderOpenRouter}}, nil))
			done <- err
		}()
		synctest.Wait()
		select {
		case err := <-done:
			require.NoError(t, err)
		default:
			t.Fatal("provider failover waited for the blocked attempt store")
		}
		assert.Equal(t, "served", rec.Body.String())
		assert.Equal(t, providers.ProviderOpenRouter, rec.Header().Get(HeaderRouterProvider))
		assert.Equal(t, plannedTestModel, rec.Header().Get(HeaderRouterModel))
	})
}

func TestAttemptSinkStoreFailureIsDiagnosticOnly(t *testing.T) {
	sink := NewAttemptSink(attemptStoreFunc(func(context.Context, InsertInferenceAttemptParams) error { return errors.New("database unavailable") }), attemptTestLog())
	sink.RecordAttempt(attemptContext(), inference.AttemptEvent{OperationID: "failed-write"})
	require.NoError(t, sink.Shutdown(context.Background()))
	assert.Equal(t, uint64(1), sink.Dropped())
}
