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

type recordingAttemptStore struct {
	mu      sync.Mutex
	got     []telemetry.InsertInferenceAttemptParams
	started chan struct{}
	block   chan struct{}
	once    sync.Once
}

func (s *recordingAttemptStore) InsertInferenceAttempt(_ context.Context, p telemetry.InsertInferenceAttemptParams) error {
	if s.started != nil {
		s.once.Do(func() { close(s.started) })
	}
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, p)
	return nil
}

func (s *recordingAttemptStore) rows() []telemetry.InsertInferenceAttemptParams {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]telemetry.InsertInferenceAttemptParams(nil), s.got...)
}

func attemptSinkContext() context.Context {
	return context.WithValue(context.Background(), requestcontext.InstallationIDContextKey{}, attemptSinkInstallationID)
}

func TestAsyncAttemptSinkDoesNotBlockRecordAttempt(t *testing.T) {
	store := &recordingAttemptStore{started: make(chan struct{}), block: make(chan struct{})}
	sink := proxy.NewAsyncAttemptSink(store, nil, 8)

	recorded := make(chan struct{})
	go func() {
		sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "op"})
		close(recorded)
	}()

	select {
	case <-recorded:
	case <-time.After(time.Second):
		t.Fatal("RecordAttempt blocked on the attempt store")
	}

	<-store.started
	close(store.block)
	require.NoError(t, sink.Close(context.Background()))

	rows := store.rows()
	require.Len(t, rows, 1)
	require.Equal(t, "op", rows[0].Event.OperationID)
	require.Equal(t, attemptSinkInstallationID, rows[0].InstallationID)
}

func TestAsyncAttemptSinkDropsWhenFullWithoutBlocking(t *testing.T) {
	store := &recordingAttemptStore{started: make(chan struct{}), block: make(chan struct{})}
	sink := proxy.NewAsyncAttemptSink(store, nil, 1)

	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "first"})
	<-store.started
	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "second"})

	recorded := make(chan struct{})
	go func() {
		sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "dropped"})
		close(recorded)
	}()
	select {
	case <-recorded:
	case <-time.After(time.Second):
		t.Fatal("RecordAttempt blocked when the queue was full")
	}

	close(store.block)
	require.NoError(t, sink.Close(context.Background()))
	require.Len(t, store.rows(), 2)
}

func TestAsyncAttemptSinkNilStoreCloses(t *testing.T) {
	sink := proxy.NewAsyncAttemptSink(nil, nil, 4)
	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "op"})
	require.NoError(t, sink.Close(context.Background()))
}

func TestAsyncAttemptSinkCloseHonorsDeadline(t *testing.T) {
	store := &recordingAttemptStore{started: make(chan struct{}), block: make(chan struct{})}
	sink := proxy.NewAsyncAttemptSink(store, nil, 1)
	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "op"})
	<-store.started

	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, sink.Close(closeCtx), context.DeadlineExceeded)

	close(store.block)
	require.NoError(t, sink.Close(context.Background()))
}
