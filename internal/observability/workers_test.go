package observability

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func testWorkers(t *testing.T) *ObservationWorkers {
	t.Helper()
	w := NewObservationWorkers()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = w.Shutdown(ctx)
	})
	return w
}

func workLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestWorkQueueCountBackpressurePreservesEveryJobAndSnapshot(t *testing.T) {
	w := testWorkers(t)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var completed atomic.Int32
	observed := make(chan string, 1)
	payload := []byte("original")
	require.True(t, w.Database.Submit(WorkTelemetry, payload, time.Minute, workLog(), func(ctx context.Context, p []byte) error {
		close(started)
		select {
		case <-release:
			observed <- string(p)
			completed.Add(1)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	<-started
	payload[0] = 'X'
	persist := func(context.Context, []byte) error { completed.Add(1); return nil }
	for range workQueueSize - 1 {
		require.True(t, w.Database.Submit(WorkTelemetry, nil, time.Second, workLog(), persist))
	}
	submitted, admitted := make(chan struct{}), make(chan bool, 1)
	go func() {
		close(submitted)
		admitted <- w.Database.Submit(WorkTelemetry, nil, time.Second, workLog(), persist)
	}()
	<-submitted
	select {
	case ok := <-admitted:
		t.Fatalf("full queue must wait for capacity, returned %v", ok)
	case <-time.After(25 * time.Millisecond):
	}
	w.Database.mu.Lock()
	assert.Equal(t, workQueueSize, w.Database.count)
	w.Database.mu.Unlock()
	unblock()
	select {
	case ok := <-admitted:
		require.True(t, ok, "saturation must not discard the waiting job")
	case <-time.After(time.Second):
		t.Fatal("producer did not wake when capacity was released")
	}
	require.Equal(t, "original", <-observed)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, w.Database.Shutdown(ctx))
	assert.EqualValues(t, workQueueSize+1, completed.Load())
	assert.Zero(t, w.Database.count)
	assert.Zero(t, w.Database.retainedBytes)
}

func TestWorkQueueByteBackpressureAndLaneIsolation(t *testing.T) {
	w := testWorkers(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var completed atomic.Int32
	block := func(ctx context.Context, _ []byte) error {
		select {
		case <-release:
			completed.Add(1)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	const chunk = workQueueBytes / 16
	payload := bytes.Repeat([]byte("x"), chunk)
	for range 16 {
		require.True(t, w.Remote.Submit(WorkOutcome, payload, time.Minute, workLog(), block))
	}
	submitted, admitted := make(chan struct{}), make(chan bool, 1)
	go func() {
		close(submitted)
		admitted <- w.Remote.Submit(WorkOutcome, []byte("x"), time.Minute, workLog(), block)
	}()
	<-submitted
	select {
	case ok := <-admitted:
		t.Fatalf("byte-saturated queue must wait, returned %v", ok)
	case <-time.After(25 * time.Millisecond):
	}
	dbDone := make(chan struct{})
	require.True(t, w.Database.Submit(WorkAttempt, nil, time.Second, workLog(), func(context.Context, []byte) error { close(dbDone); return nil }))
	select {
	case <-dbDone:
	case <-time.After(time.Second):
		t.Fatal("remote backpressure blocked the database lane")
	}
	w.Remote.mu.Lock()
	assert.Equal(t, workQueueBytes, w.Remote.retainedBytes)
	w.Remote.mu.Unlock()
	unblock()
	select {
	case ok := <-admitted:
		require.True(t, ok)
	case <-time.After(time.Second):
		t.Fatal("producer did not wake when bytes were released")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, w.Remote.Shutdown(ctx))
	assert.EqualValues(t, 17, completed.Load())
	assert.Zero(t, w.Remote.retainedBytes)
}

// A job larger than the lane's byte budget is never discarded: it waits for an
// empty lane and is admitted alone, so retained bytes stay bounded at one job.
func TestWorkQueueOversizedJobWaitsForEmptyLaneInsteadOfDropping(t *testing.T) {
	w := testWorkers(t)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	require.True(t, w.Remote.Submit(WorkOutcome, []byte("small"), time.Minute, workLog(), func(ctx context.Context, _ []byte) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	<-started
	oversized := bytes.Repeat([]byte("x"), workQueueBytes+1)
	delivered := make(chan int, 1)
	submitted, admitted := make(chan struct{}), make(chan bool, 1)
	go func() {
		close(submitted)
		admitted <- w.Remote.Submit(WorkOutcome, oversized, time.Minute, workLog(), func(_ context.Context, p []byte) error { delivered <- len(p); return nil })
	}()
	<-submitted
	select {
	case ok := <-admitted:
		t.Fatalf("oversized job must wait for an empty lane, returned %v", ok)
	case <-time.After(25 * time.Millisecond):
	}
	unblock()
	select {
	case ok := <-admitted:
		require.True(t, ok, "size must never reject a job")
	case <-time.After(time.Second):
		t.Fatal("oversized job was not admitted once the lane emptied")
	}
	select {
	case n := <-delivered:
		assert.Equal(t, len(oversized), n, "the whole payload must be delivered, never truncated")
	case <-time.After(time.Second):
		t.Fatal("oversized job never executed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, w.Remote.Shutdown(ctx))
	assert.Zero(t, w.Remote.retainedBytes)
}

func TestWorkQueueFixedConcurrencyAndShutdownCancellation(t *testing.T) {
	w := testWorkers(t)
	var active, peak atomic.Int32
	started := make(chan struct{}, 2)
	canceled := make(chan struct{}, 2)
	for range 20 {
		require.True(t, w.Remote.Submit(WorkOutcome, nil, time.Minute, workLog(), func(ctx context.Context, _ []byte) error {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			started <- struct{}{}
			<-ctx.Done()
			canceled <- struct{}{}
			return ctx.Err()
		}))
	}
	<-started
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, w.Remote.Shutdown(ctx), context.DeadlineExceeded)
	<-canceled
	<-canceled
	require.NoError(t, w.Remote.Shutdown(context.Background()))
	assert.EqualValues(t, 2, peak.Load())
	assert.Zero(t, w.Remote.retainedBytes)
	assert.False(t, w.Remote.Submit(WorkOutcome, nil, time.Second, workLog(), nil))
}

func TestWorkQueuePanicFailureAndDeadlineDoNotRemoveWorker(t *testing.T) {
	w := testWorkers(t)
	require.True(t, w.Database.Submit(WorkTelemetry, nil, time.Second, workLog(), func(context.Context, []byte) error { panic("secret content must not be logged") }))
	require.True(t, w.Database.Submit(WorkTelemetry, nil, time.Second, workLog(), func(context.Context, []byte) error { return errors.New("failed") }))
	require.True(t, w.Database.Submit(WorkTelemetry, nil, time.Millisecond, workLog(), func(ctx context.Context, _ []byte) error { <-ctx.Done(); return ctx.Err() }))
	done := make(chan struct{})
	require.True(t, w.Database.Submit(WorkTelemetry, nil, time.Second, workLog(), func(context.Context, []byte) error { close(done); return nil }))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker stopped processing after failure")
	}
}

func TestWorkQueueConcurrentSubmissionAndRepeatedShutdown(t *testing.T) {
	w := testWorkers(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 300 {
				w.Database.Submit(WorkAttempt, []byte("attempt"), time.Second, workLog(), func(context.Context, []byte) error { return nil })
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = w.Shutdown(context.Background()) }()
	}
	wg.Wait()
	assert.Zero(t, w.Database.retainedBytes)
	assert.Zero(t, w.Database.count)
}

func TestWorkQueueCountsEveryDropButRateLimitsLogs(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil)).With("request_id", "request-test")
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	counter, err := provider.Meter("test").Int64Counter("router.observations.dropped")
	require.NoError(t, err)
	q := &WorkQueue{counter: counter, lane: workRemote}
	for range 3 {
		q.Reject(WorkFeedback, log, errors.New("invalid sample"))
	}
	q.record(log, WorkOutcome, workFailed, errors.New("endpoint unavailable"))
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))
	require.Len(t, collected.ScopeMetrics, 1)
	points := collected.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64]).DataPoints
	require.Len(t, points, 2)
	totals := map[string]int64{}
	for _, point := range points {
		require.Equal(t, 3, point.Attributes.Len(), "only bounded lane/kind/reason dimensions")
		reason, _ := point.Attributes.Value(attribute.Key("reason"))
		totals[reason.AsString()] = point.Value
	}
	assert.Equal(t, map[string]int64{"invalid_payload": 3, "failed": 1}, totals)
	assert.Equal(t, 1, strings.Count(logs.String(), "Optional observation dropped"))
	assert.Contains(t, logs.String(), "request-test")
}

func TestWorkQueueShutdownReleasesEveryBlockedProducer(t *testing.T) {
	w := testWorkers(t)
	started := make(chan struct{})
	require.True(t, w.Database.Submit(WorkAttempt, nil, time.Minute, workLog(), func(ctx context.Context, _ []byte) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	<-started
	for range workQueueSize - 1 {
		require.True(t, w.Database.Submit(WorkTelemetry, nil, time.Second, workLog(), func(context.Context, []byte) error { return nil }))
	}
	const producers = 8
	submitting, admissions := make(chan struct{}, producers), make(chan bool, producers)
	for range producers {
		go func() {
			submitting <- struct{}{}
			admissions <- w.Database.Submit(WorkTelemetry, nil, time.Second, workLog(), func(context.Context, []byte) error { return nil })
		}()
	}
	for range producers {
		<-submitting
	}
	select {
	case ok := <-admissions:
		t.Fatalf("full queue must backpressure producers before shutdown, returned %v", ok)
	case <-time.After(25 * time.Millisecond):
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, w.Database.Shutdown(ctx), context.DeadlineExceeded)
	for range producers {
		select {
		case ok := <-admissions:
			assert.False(t, ok, "shutdown must reject blocked submissions")
		case <-time.After(time.Second):
			t.Fatal("shutdown left a producer waiting for capacity")
		}
	}
	drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
	defer drainCancel()
	require.NoError(t, w.Database.Shutdown(drainCtx))
	assert.Zero(t, w.Database.count)
	assert.Zero(t, w.Database.retainedBytes)
}

func TestWorkQueueExecutionDeadlineStartsAfterQueueWait(t *testing.T) {
	w := testWorkers(t)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	require.True(t, w.Database.Submit(WorkAttempt, nil, time.Minute, workLog(), func(ctx context.Context, _ []byte) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	<-started
	observed := make(chan error, 1)
	require.True(t, w.Database.Submit(WorkTelemetry, nil, 10*time.Millisecond, workLog(), func(ctx context.Context, _ []byte) error {
		observed <- ctx.Err()
		return nil
	}))
	select {
	case <-observed:
		t.Fatal("second job ran before the first released the worker")
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-observed:
		assert.NoError(t, err, "queued time must not consume the execution deadline")
	case <-time.After(time.Second):
		t.Fatal("queued job was discarded instead of executed")
	}
}
