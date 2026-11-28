package proxy_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/telemetry"
)

const (
	attemptSinkInstallationID    = "11111111-1111-1111-1111-111111111111"
	attemptSinkInstallationIDTwo = "22222222-2222-2222-2222-222222222222"
)

type attemptSinkTestKey struct{}

type recordedCorrelation struct {
	sessionID string
	traceID   trace.TraceID
	extraVal  string
	canceled  bool
}

type recordingAttemptStore struct {
	mu           sync.Mutex
	got          []telemetry.InsertInferenceAttemptParams
	correlations []recordedCorrelation
	started      chan struct{}
	entered      chan string
	block        chan struct{}
	startedOnce  sync.Once
	releaseOnce  sync.Once
}

func newBlockingAttemptStore() *recordingAttemptStore {
	return &recordingAttemptStore{
		started: make(chan struct{}),
		entered: make(chan string, 16),
		block:   make(chan struct{}),
	}
}

func (s *recordingAttemptStore) InsertInferenceAttempt(ctx context.Context, p telemetry.InsertInferenceAttemptParams) error {
	if s.started != nil {
		s.startedOnce.Do(func() { close(s.started) })
	}
	if s.entered != nil {
		select {
		case s.entered <- p.InstallationID:
		default:
		}
	}
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	extraVal, _ := ctx.Value(attemptSinkTestKey{}).(string)
	s.correlations = append(s.correlations, recordedCorrelation{
		sessionID: observability.ClientSessionIDFromContext(ctx),
		traceID:   trace.SpanContextFromContext(ctx).TraceID(),
		extraVal:  extraVal,
		canceled:  ctx.Err() != nil,
	})
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

func (s *recordingAttemptStore) seenCorrelations() []recordedCorrelation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedCorrelation(nil), s.correlations...)
}

func attemptSinkContext() context.Context {
	return attemptSinkContextFor(attemptSinkInstallationID)
}

func attemptSinkContextFor(installationID string) context.Context {
	return context.WithValue(context.Background(), requestcontext.InstallationIDContextKey{}, installationID)
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

func TestAsyncAttemptSinkKeepsOnlyCorrelationContext(t *testing.T) {
	store := newBlockingAttemptStore()
	sink := proxy.NewAsyncAttemptSink(store, nil, 8)

	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	reqCtx, cancel := context.WithCancel(attemptSinkContext())
	reqCtx = trace.ContextWithSpanContext(reqCtx, spanCtx)
	reqCtx = observability.WithClientSessionID(reqCtx, "session-123")
	reqCtx = context.WithValue(reqCtx, attemptSinkTestKey{}, "large-request-payload")

	sink.RecordAttempt(reqCtx, inference.AttemptEvent{OperationID: "op"})
	cancel()
	store.release()
	require.NoError(t, sink.Close(context.Background()))

	rows := store.rows()
	require.Len(t, rows, 1)
	require.Equal(t, []recordedCorrelation{{
		sessionID: "session-123",
		traceID:   traceID,
		extraVal:  "",
		canceled:  false,
	}}, store.seenCorrelations())
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

func TestAsyncAttemptSinkLimitsSingleInstallationBacklog(t *testing.T) {
	store := newBlockingAttemptStore()
	t.Cleanup(store.release)
	sink := proxy.NewAsyncAttemptSink(store, nil, 8)

	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "in-flight"})
	waitForAttemptSignal(t, store.started)
	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "queued-1"})
	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "queued-2"})
	sink.RecordAttempt(attemptSinkContext(), inference.AttemptEvent{OperationID: "dropped"})

	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := sink.Close(closeCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "queued=2 in_flight=1 dropped=1")

	store.release()
	require.NoError(t, sink.Close(context.Background()))

	rows := store.rows()
	require.Len(t, rows, 3)
	require.Equal(t, "in-flight", rows[0].Event.OperationID)
	require.Equal(t, "queued-1", rows[1].Event.OperationID)
	require.Equal(t, "queued-2", rows[2].Event.OperationID)
}

func TestAsyncAttemptSinkIsolatesInstallationsUnderBurst(t *testing.T) {
	store := newBlockingAttemptStore()
	t.Cleanup(store.release)
	sink := proxy.NewAsyncAttemptSink(store, nil, 2)

	sink.RecordAttempt(attemptSinkContextFor(attemptSinkInstallationID), inference.AttemptEvent{OperationID: "inst1-in-flight"})
	waitForEntered(t, store.entered, attemptSinkInstallationID)
	sink.RecordAttempt(attemptSinkContextFor(attemptSinkInstallationID), inference.AttemptEvent{OperationID: "inst1-q1"})
	sink.RecordAttempt(attemptSinkContextFor(attemptSinkInstallationID), inference.AttemptEvent{OperationID: "inst1-q2"})
	sink.RecordAttempt(attemptSinkContextFor(attemptSinkInstallationID), inference.AttemptEvent{OperationID: "inst1-dropped"})

	sink.RecordAttempt(attemptSinkContextFor(attemptSinkInstallationIDTwo), inference.AttemptEvent{OperationID: "inst2-in-flight"})
	waitForEntered(t, store.entered, attemptSinkInstallationIDTwo)
	sink.RecordAttempt(attemptSinkContextFor(attemptSinkInstallationIDTwo), inference.AttemptEvent{OperationID: "inst2-q1"})
	sink.RecordAttempt(attemptSinkContextFor(attemptSinkInstallationIDTwo), inference.AttemptEvent{OperationID: "inst2-dropped"})

	store.release()
	require.NoError(t, sink.Close(context.Background()))

	var opIDs []string
	for _, row := range store.rows() {
		opIDs = append(opIDs, row.Event.OperationID)
	}
	require.ElementsMatch(t, []string{"inst1-in-flight", "inst2-in-flight", "inst1-q1", "inst2-q1"}, opIDs)
}

func waitForEntered(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case got := <-ch:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for installation %s to enter store", want)
		}
	}
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
	err := sink.Close(closeCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "queued=0 in_flight=1 dropped=0")

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
