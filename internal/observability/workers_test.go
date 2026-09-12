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

func TestWorkQueueCountIncludesRunningJobAndSnapshotsPayload(t *testing.T) {
	w := testWorkers(t)
	started, release := make(chan struct{}), make(chan struct{})
	observed := make(chan string, 1)
	payload := []byte("original")
	require.True(t, w.Database.Submit(WorkTelemetry, payload, time.Second, workLog(), func(ctx context.Context, p []byte) error {
		close(started)
		select {
		case <-release:
			observed <- string(p)
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}))
	<-started
	payload[0] = 'X'
	for range workQueueSize - 1 {
		require.True(t, w.Database.Submit(WorkTelemetry, nil, time.Second, workLog(), func(context.Context, []byte) error { return nil }))
	}
	require.False(t, w.Database.Submit(WorkTelemetry, nil, time.Second, workLog(), func(context.Context, []byte) error { t.Error("rejected job ran"); return nil }))
	close(release)
	require.Equal(t, "original", <-observed)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, w.Database.Shutdown(ctx))
	assert.Zero(t, w.Database.count)
	assert.Zero(t, w.Database.retainedBytes)
}

func TestWorkQueueByteBoundAndLaneIsolation(t *testing.T) {
	w := testWorkers(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	block := func(ctx context.Context, _ []byte) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	payload := bytes.Repeat([]byte("x"), MaxWorkPayloadBytes)
	for range workQueueBytes / MaxWorkPayloadBytes {
		require.True(t, w.Remote.Submit(WorkOutcome, payload, time.Second, workLog(), block))
	}
	assert.False(t, w.Remote.Submit(WorkOutcome, []byte("x"), time.Second, workLog(), block))
	assert.False(t, w.Database.Submit(WorkAttempt, append(payload, 'x'), time.Second, workLog(), block))
	dbDone := make(chan struct{})
	require.True(t, w.Database.Submit(WorkAttempt, nil, time.Second, workLog(), func(context.Context, []byte) error { close(dbDone); return nil }))
	select {
	case <-dbDone:
	case <-time.After(time.Second):
		t.Fatal("remote saturation blocked database worker")
	}
	w.Remote.mu.Lock()
	assert.Equal(t, workQueueBytes, w.Remote.retainedBytes)
	w.Remote.mu.Unlock()
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

func TestWorkQueueDropsExpiredJobsBeforeIO(t *testing.T) {
	w := testWorkers(t)
	started, release := make(chan struct{}), make(chan struct{})
	require.True(t, w.Database.Submit(WorkAttempt, nil, time.Second, workLog(), func(ctx context.Context, _ []byte) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	<-started
	var writes atomic.Int32
	require.True(t, w.Database.Submit(WorkTelemetry, nil, time.Second, workLog(), func(context.Context, []byte) error { writes.Add(1); return nil }))
	queued := <-w.Database.queue
	queued.submitted = time.Now().Add(-workMaxAge - time.Second)
	w.Database.queue <- queued
	close(release)
	require.NoError(t, w.Database.Shutdown(context.Background()))
	assert.Zero(t, writes.Load())
	assert.Zero(t, w.Database.retainedBytes)
}
