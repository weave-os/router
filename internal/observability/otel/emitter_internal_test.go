package otel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func stringValue(s string) *commonv1.AnyValue {
	return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: s}}
}

func TestEmitter_ByteBudgetIncludesBothBlockedExports(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	var active, peak, requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- r.URL.Path
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	em, err := NewEmitter(EmitterConfig{
		Endpoint: server.URL, Workers: 1, QueueSize: 1000, BatchSize: 1,
		ExportTimeout: time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, em.Shutdown(context.Background())) })

	// One maximum-sized captured request plus response per record.
	attrs := NewAttrBuilder(2).
		String("io.request_body", strings.Repeat("q", 1<<20)).
		String("io.response_body", strings.Repeat("a", 1<<20)).Build()
	span := &tracev1.Span{Name: "captured", Attributes: attrs}
	log := &logsv1.LogRecord{Body: stringValue("router.call"), Attributes: attrs}
	em.Enqueue(span)
	em.EnqueueLog(log)
	for range 2 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("export did not start")
		}
	}
	finished := make(chan struct{})
	go func() {
		for range 1000 {
			em.Enqueue(span)
			em.EnqueueLog(log)
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("admission waited for blocked export")
	}
	assert.Greater(t, em.retained.Load(), int64(60<<20))
	assert.LessOrEqual(t, em.retained.Load(), int64(64<<20))
	assert.Greater(t, em.dropped.Load(), int64(1980))
	assert.Less(t, len(em.queue)+len(em.logQueue), 20, "byte capacity, not 1000-record capacity, must bind")
	assert.Equal(t, int64(2), peak.Load())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, em.Shutdown(ctx), context.DeadlineExceeded)
	select {
	case <-em.done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown left exporter workers running")
	}
	assert.Zero(t, em.retained.Load(), "queued and in-flight reservations must be released")
	assert.Equal(t, int64(2), requests.Load(), "shutdown must discard, not export, the backlog")
	require.Eventually(t, func() bool { return active.Load() == 0 }, time.Second, time.Millisecond)
}

func TestEmitter_BatchByteCapAndSnapshotOwnership(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	var contents []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}
		var exported collogspb.ExportLogsServiceRequest
		if !assert.NoError(t, proto.Unmarshal(body, &exported)) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		sizes = append(sizes, len(body))
		for _, resource := range exported.ResourceLogs {
			for _, scope := range resource.ScopeLogs {
				for _, record := range scope.LogRecords {
					contents = append(contents, record.Body.GetStringValue())
				}
			}
		}
	}))
	defer server.Close()
	em, err := NewEmitter(EmitterConfig{
		Endpoint: server.URL, Workers: 1, QueueSize: 100, BatchSize: 50, FlushInterval: time.Minute,
	})
	require.NoError(t, err)
	original := strings.Repeat("a", 1<<20)
	record := &logsv1.LogRecord{Body: stringValue(original)}
	for range 8 {
		em.EnqueueLog(record)
	}
	record.Body.Value = &commonv1.AnyValue_StringValue{StringValue: "mutated"}
	require.NoError(t, em.Shutdown(context.Background()))
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []int{3, 3, 2}, func() []int {
		counts := make([]int, len(sizes))
		for i, size := range sizes {
			assert.LessOrEqual(t, size, 4<<20)
			counts[i] = size / (1 << 20)
		}
		return counts
	}())
	require.Len(t, contents, 8)
	for _, content := range contents {
		assert.Equal(t, original, content)
	}
	assert.Zero(t, em.retained.Load())
	assert.Zero(t, em.dropped.Load())
}

func TestEmitter_ReleasesRejectedAndFailedRecords(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	em, err := NewEmitter(EmitterConfig{Endpoint: server.URL, Workers: 1, BatchSize: 1})
	require.NoError(t, err)
	em.EnqueueLog(&logsv1.LogRecord{Body: stringValue(strings.Repeat("x", 4<<20))})
	em.Enqueue(&tracev1.Span{Name: strings.Repeat("x", 32<<20)})
	em.EnqueueLog(&logsv1.LogRecord{Body: stringValue(string([]byte{0xff}))})
	assert.Equal(t, int64(3), em.dropped.Load())
	assert.Zero(t, em.retained.Load())
	em.EnqueueLog(&logsv1.LogRecord{Body: stringValue("valid")})
	require.NoError(t, em.Shutdown(context.Background()))
	assert.Equal(t, int64(1), requests.Load(), "failed export is not retried")
	assert.Zero(t, em.retained.Load())
	em.Enqueue(&tracev1.Span{Name: "late"})
	em.EnqueueLog(&logsv1.LogRecord{Body: stringValue("late")})
	assert.Equal(t, int64(5), em.dropped.Load())
	assert.Zero(t, em.retained.Load())
}

func TestEmitter_ExportTimeoutReleasesCapacity(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	em, err := NewEmitter(EmitterConfig{
		Endpoint: server.URL, Workers: 1, BatchSize: 1, ExportTimeout: 30 * time.Millisecond,
	})
	require.NoError(t, err)
	em.EnqueueLog(&logsv1.LogRecord{Body: stringValue("times out")})
	require.Eventually(t, func() bool { return em.retained.Load() == 0 }, 2*time.Second, time.Millisecond)
	// No shutdown deadline is needed: the per-export deadline also releases work.
	require.NoError(t, em.Shutdown(context.Background()))
}

func TestEmitter_ConcurrentAdmissionAndShutdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer server.Close()
	em, err := NewEmitter(EmitterConfig{Endpoint: server.URL, Workers: 2, QueueSize: 5, BatchSize: 3})
	require.NoError(t, err)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			<-start
			for range 100 {
				em.Enqueue(&tracev1.Span{Name: "concurrent"})
				em.EnqueueLog(&logsv1.LogRecord{Body: stringValue("concurrent")})
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			<-start
			assert.NoError(t, em.Shutdown(context.Background()))
		})
	}
	close(start)
	wg.Wait()
	assert.Zero(t, em.retained.Load())
	assert.Empty(t, em.queue)
	assert.Empty(t, em.logQueue)
}
