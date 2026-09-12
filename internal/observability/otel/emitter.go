package otel

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"weave-os/router/internal/observability"
)

const (
	maxBufferedBytes = 64 << 20
	maxExportBytes   = 4 << 20
)

// EmitterConfig controls the emitter's async export pipeline.
type EmitterConfig struct {
	Endpoint      string
	Headers       map[string]string
	ServiceName   string
	ResourceAttrs map[string]string
	Workers       int
	QueueSize     int
	BatchSize     int
	FlushInterval time.Duration
	ExportTimeout time.Duration
}

type queuedRecord struct {
	body     []byte
	reserved int64
}

// Emitter batches OTLP spans and logs for best-effort HTTP export. Safe for
// concurrent use. A nil *Emitter means OTel is disabled; all methods no-op.
type Emitter struct {
	queue       chan queuedRecord
	logQueue    chan queuedRecord
	client      *http.Client
	endpoint    string
	logEndpoint string
	headers     map[string]string
	envelope    batchEnvelope
	batchSz     int
	flushInt    time.Duration
	retained    atomic.Int64
	dropped     atomic.Int64
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	closeMu     sync.RWMutex
	closed      bool
}

// NewEmitter starts the worker pool. Returns (nil, nil) when Endpoint is empty.
func NewEmitter(cfg EmitterConfig) (*Emitter, error) {
	if cfg.Endpoint == "" {
		return nil, nil
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1000
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 500 * time.Millisecond
	}
	if cfg.ExportTimeout <= 0 {
		cfg.ExportTimeout = 10 * time.Second
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = "router"
	}

	envelope, err := newBatchEnvelope(buildResource(cfg.ServiceName, cfg.ResourceAttrs), &commonv1.InstrumentationScope{Name: "workweave-router"})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Emitter{
		queue:       make(chan queuedRecord, cfg.QueueSize),
		logQueue:    make(chan queuedRecord, cfg.QueueSize),
		client:      &http.Client{Timeout: cfg.ExportTimeout},
		endpoint:    strings.TrimRight(cfg.Endpoint, "/") + "/v1/traces",
		logEndpoint: strings.TrimRight(cfg.Endpoint, "/") + "/v1/logs",
		headers:     maps.Clone(cfg.Headers),
		envelope:    envelope,
		batchSz:     cfg.BatchSize,
		flushInt:    cfg.FlushInterval,
		ctx:         ctx,
		cancel:      cancel,
		done:        make(chan struct{}),
	}

	e.wg.Add(cfg.Workers * 2)
	for range cfg.Workers {
		go e.worker(e.queue, e.endpoint)
		go e.worker(e.logQueue, e.logEndpoint)
	}
	go func() {
		e.wg.Wait()
		e.cancel()
		e.client.CloseIdleConnections()
		if n := e.dropped.Load(); n > 0 {
			observability.Get().Warn("Dropped OTLP records during emitter lifetime", "count", n)
		}
		close(e.done)
	}()
	return e, nil
}

// NewBuffer returns a request-scoped span/log buffer, or nil when disabled.
func (e *Emitter) NewBuffer() *Buffer { return NewBuffer(e) }

// Enqueue snapshots s before returning. It never waits for export or capacity;
// records exceeding the count/byte limits or submitted after shutdown drop.
func (e *Emitter) Enqueue(s *tracev1.Span) {
	if e != nil {
		e.enqueue(e.queue, s)
	}
}

// EnqueueLog has the same admission and ownership contract as Enqueue.
func (e *Emitter) EnqueueLog(r *logsv1.LogRecord) {
	if e != nil {
		e.enqueue(e.logQueue, r)
	}
}

func (e *Emitter) enqueue(queue chan<- queuedRecord, record proto.Message) {
	e.closeMu.RLock()
	defer e.closeMu.RUnlock()
	if e.closed {
		e.dropped.Add(1)
		return
	}

	size := proto.Size(record)
	exportSize := e.envelope.size(recordFieldSize(size))
	if exportSize > maxExportBytes {
		e.dropped.Add(1)
		return
	}
	// Reserve before serialization, covering concurrent producers, queued and
	// in-flight records, and the second copy in the HTTP batch. Per-record
	// envelope overhead overestimates the one shared envelope in a batch.
	reserved := int64(2 * exportSize)
	for {
		retained := e.retained.Load()
		if reserved > maxBufferedBytes-retained {
			e.dropped.Add(1)
			return
		}
		if e.retained.CompareAndSwap(retained, retained+reserved) {
			break
		}
	}
	body, err := proto.MarshalOptions{}.MarshalAppend(make([]byte, 0, size), record)
	if err != nil {
		e.retained.Add(-reserved)
		e.dropped.Add(1)
		observability.Get().Warn("Failed to marshal OTLP record", "err", err)
		return
	}
	select {
	case queue <- queuedRecord{body: body, reserved: reserved}:
	default:
		e.retained.Add(-reserved)
		e.dropped.Add(1)
	}
}

// Shutdown stops admission and drains within ctx. Expiration cancels active
// HTTP exports and discards pending records. Repeated calls can await cleanup.
func (e *Emitter) Shutdown(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.closeMu.Lock()
	if !e.closed {
		e.closed = true
		close(e.queue)
		close(e.logQueue)
	}
	e.closeMu.Unlock()

	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		e.cancel()
		return ctx.Err()
	}
}

func (e *Emitter) worker(queue <-chan queuedRecord, endpoint string) {
	defer e.wg.Done()
	batch := make([]queuedRecord, 0, min(e.batchSz, cap(queue)))
	batchBytes := 0
	timer := time.NewTimer(e.flushInt)
	defer timer.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if e.ctx.Err() == nil {
			e.post(endpoint, e.envelope.marshal(batch, batchBytes))
		} else {
			e.dropped.Add(int64(len(batch)))
		}
		for i := range batch {
			reserved := batch[i].reserved
			batch[i] = queuedRecord{}
			e.retained.Add(-reserved)
		}
		batch = batch[:0]
		batchBytes = 0
		timer.Reset(e.flushInt)
	}

	for {
		select {
		case record, ok := <-queue:
			if !ok {
				flush()
				return
			}
			if e.ctx.Err() != nil {
				e.retained.Add(-record.reserved)
				e.dropped.Add(1)
				continue
			}
			size := recordFieldSize(len(record.body))
			if e.envelope.size(batchBytes+size) > maxExportBytes {
				flush()
			}
			batch = append(batch, record)
			batchBytes += size
			if len(batch) >= e.batchSz {
				flush()
			}
		case <-timer.C:
			flush()
			timer.Reset(e.flushInt)
		}
	}
}

func (e *Emitter) post(endpoint string, body []byte) {
	httpReq, err := http.NewRequestWithContext(e.ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		observability.Get().Warn("Failed to create OTLP export HTTP request", "err", err)
		return
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	for k, v := range e.headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := e.client.Do(httpReq)
	if err != nil {
		observability.Get().Warn("OTLP export request failed", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		observability.Get().Warn("OTLP export returned non-2xx status", "status", resp.StatusCode)
	}
}
