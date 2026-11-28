package proxy_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/telemetry"
)

const attemptSinkInstallationID = "11111111-1111-1111-1111-111111111111"

type attemptSinkTestKey struct{}

type recordingAttemptStore struct {
	mu          sync.Mutex
	got         []telemetry.InsertInferenceAttemptParams
	values      []string
	started     chan struct{}
	block       chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func newBlockingAttemptStore() *recordingAttemptStore {
	return &recordingAttemptStore{started: make(chan struct{}), block: make(chan struct{})}
}

func (s *recordingAttemptStore) InsertInferenceAttempt(ctx context.Context, p telemetry.InsertInferenceAttemptParams) error {
	if s.started != nil {
		s.startedOnce.Do(func() { close(s.started) })
	}
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, _ := ctx.Value(attemptSinkTestKey{}).(string); v != "" {
		s.values = append(s.values, v)
	}
	s.got = append(s.got, p)
	return nil
}

func (s *recordingAttemptStore) release() {
	s.releaseOnce.Do(func() {
		if s.block != nil {
			close(s.block)
		}
	})
}

func (s *recordingAttemptStore) rows() []telemetry.InsertInferenceAttemptParams {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]telemetry.InsertInferenceAttemptParams(nil), s.got...)
}

func (s *recordingAttemptStore) seenValues() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.values...)
}

func attemptSinkContext() context.Context {
	return context.WithValue(context.Background(), requestcontext.InstallationIDContextKey{}, attemptSinkInstallationID)
}

func waitForAttemptSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the attempt store")
	}
}

func TestAsyncAttemptSinkDoesNotBlockRecordAttempt(t *testing.T) {
	store := newBlockingAttemptStore()
	t.Cleanup(store.release)
	sink := proxy.NewAsyncAttemptSink(store, nil, 8)

	recorded := make(chan struct{})
	go func() {
		sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "op"})
		close(recorded)
	}()
	waitForAttemptSignal(t, recorded)
	waitForAttemptSignal(t, store.started)

	store.release()
	require.NoError(t, sink.Close(context.Background()))

	rows := store.rows()
	require.Len(t, rows, 1)
	require.Equal(t, "op", rows[0].Event.OperationID)
	require.Equal(t, attemptSinkInstallationID, rows[0].InstallationID)
}

func TestAsyncAttemptSinkPreservesRequestContextValues(t *testing.T) {
	store := &recordingAttemptStore{}
	sink := proxy.NewAsyncAttemptSink(store, nil, 8)

	ctx := context.WithValue(attemptSinkContext(), attemptSinkTestKey{}, "session-123")
	sink.RecordAttempt(ctx, inference.AttemptEvent{OperationID: "op"})
	require.NoError(t, sink.Close(context.Background()))

	rows := store.rows()
	require.Len(t, rows, 1)
	require.Equal(t, []string{"session-123"}, store.seenValues())
}

func TestAsyncAttemptSinkDropsWhenFullWithoutBlocking(t *testing.T) {
	store := newBlockingAttemptStore()
	t.Cleanup(store.release)
	sink := proxy.NewAsyncAttemptSink(store, nil, 1)

	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "first"})
	waitForAttemptSignal(t, store.started)
	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "second"})

	recorded := make(chan struct{})
	go func() {
		sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "dropped"})
		close(recorded)
	}()
	waitForAttemptSignal(t, recorded)

	store.release()
	require.NoError(t, sink.Close(context.Background()))
	require.Len(t, store.rows(), 2)
}

func TestAsyncAttemptSinkRecoversFromStorePanic(t *testing.T) {
	store := &panicAttemptStore{panicked: make(chan struct{})}
	sink := proxy.NewAsyncAttemptSink(store, nil, 8)

	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "panic"})
	waitForAttemptSignal(t, store.panicked)
	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "ok"})
	require.NoError(t, sink.Close(context.Background()))
	require.Equal(t, []string{"ok"}, store.rows())
}

func TestAsyncAttemptSinkNilStoreCloses(t *testing.T) {
	sink := proxy.NewAsyncAttemptSink(nil, nil, 4)
	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "op"})
	require.NoError(t, sink.Close(context.Background()))
}

func TestAsyncAttemptSinkCloseHonorsDeadline(t *testing.T) {
	store := newBlockingAttemptStore()
	t.Cleanup(store.release)
	sink := proxy.NewAsyncAttemptSink(store, nil, 1)
	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "op"})
	waitForAttemptSignal(t, store.started)

	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, sink.Close(closeCtx), context.DeadlineExceeded)

	store.release()
	require.NoError(t, sink.Close(context.Background()))
}

type panicAttemptStore struct {
	mu       sync.Mutex
	got      []string
	panicked chan struct{}
	once     sync.Once
}

func (s *panicAttemptStore) InsertInferenceAttempt(_ context.Context, p telemetry.InsertInferenceAttemptParams) error {
	if p.Event.OperationID == "panic" {
		s.once.Do(func() { close(s.panicked) })
		panic("attempt store panic")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, p.Event.OperationID)
	return nil
}

func (s *panicAttemptStore) rows() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}
