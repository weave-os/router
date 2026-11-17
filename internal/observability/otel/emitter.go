package otel

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"weave-os/router/internal/telemetry"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"weave-os/router/internal/observability"
)

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

type Emitter struct {
	queue        chan *tracev1.Span
	logQueue     chan *logsv1.LogRecord
	client       *http.Client
	endpoint     string
	logEndpoint  string
	headers      map[string]string
	resource     *resourcev1.Resource
	batchSz      int
	flushInt     time.Duration
	dropped      atomic.Int64
	wg           sync.WaitGroup
	closeMu      sync.RWMutex
	closed       atomic.Bool
	exportCtx    context.Context
	cancelExport context.CancelFunc
}

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

	exportCtx, cancelExport := context.WithCancel(context.Background())
	e := &Emitter{
		exportCtx:    exportCtx,
		cancelExport: cancelExport,
		queue:        make(chan *tracev1.Span, cfg.QueueSize),
		logQueue:     make(chan *logsv1.LogRecord, cfg.QueueSize),
		client:       &http.Client{Timeout: cfg.ExportTimeout},
		endpoint:     strings.TrimRight(cfg.Endpoint, "/") + "/v1/traces",
		logEndpoint:  strings.TrimRight(cfg.Endpoint, "/") + "/v1/logs",
		headers:      cfg.Headers,
		resource:     buildResource(cfg.ServiceName, cfg.ResourceAttrs),
		batchSz:      cfg.BatchSize,
		flushInt:     cfg.FlushInterval,
	}

	e.wg.Add(cfg.Workers * 2)
	for range cfg.Workers {
		go e.worker()
		go e.logWorker()
	}

	return e, nil
}

func (e *Emitter) NewBuffer() *Buffer {
	return NewBuffer(e)
}

func (e *Emitter) NewRecorder() telemetry.Recorder {
	if e == nil {
		return nil
	}
	return NewBuffer(e)
}

func (e *Emitter) NewUsageExtractor(inner http.ResponseWriter, provider string) (*telemetry.UsageExtractor, http.ResponseWriter) {
	wrapper := NewUsageExtractor(inner, provider)
	return wrapper.Extractor(), wrapper
}

func (e *Emitter) Enqueue(s *tracev1.Span) {
	if e == nil {
		return
	}
	e.closeMu.RLock()
	defer e.closeMu.RUnlock()
	if e.closed.Load() {
		e.dropped.Add(1)
		return
	}
	select {
	case e.queue <- s:
	default:
		e.dropped.Add(1)
	}
}

func (e *Emitter) EnqueueLog(r *logsv1.LogRecord) {
	if e == nil {
		return
	}
	e.closeMu.RLock()
	defer e.closeMu.RUnlock()
	if e.closed.Load() {
		e.dropped.Add(1)
		return
	}
	select {
	case e.logQueue <- r:
	default:
		e.dropped.Add(1)
	}
}

func (e *Emitter) Shutdown(c context.Context) error {
	if e == nil {
		return nil
	}

	e.closeMu.Lock()
	if e.closed.Swap(true) {
		e.closeMu.Unlock()
		return nil
	}
	close(e.queue)
	close(e.logQueue)
	e.closeMu.Unlock()

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		e.cancelExport()
	case <-c.Done():
		e.cancelExport()
		return c.Err()
	}

	if n := e.dropped.Load(); n > 0 {
		observability.Get().Warn("Dropped spans during emitter lifetime", "count", n)
	}

	return nil
}

func (e *Emitter) worker() {
	defer e.wg.Done()

	batch := make([]*tracev1.Span, 0, e.batchSz)
	timer := time.NewTimer(e.flushInt)
	defer timer.Stop()

	for {
		select {
		case span, ok := <-e.queue:
			if !ok {
				if len(batch) > 0 {
					e.exportBatch(batch)
				}
				return
			}
			batch = append(batch, span)
			if len(batch) >= e.batchSz {
				e.exportBatch(batch)
				batch = batch[:0]
				timer.Reset(e.flushInt)
			}

		case <-timer.C:
			if len(batch) > 0 {
				e.exportBatch(batch)
				batch = batch[:0]
			}
			timer.Reset(e.flushInt)
		}
	}
}

func (e *Emitter) logWorker() {
	defer e.wg.Done()

	batch := make([]*logsv1.LogRecord, 0, e.batchSz)
	timer := time.NewTimer(e.flushInt)
	defer timer.Stop()

	for {
		select {
		case rec, ok := <-e.logQueue:
			if !ok {
				if len(batch) > 0 {
					e.exportLogBatch(batch)
				}
				return
			}
			batch = append(batch, rec)
			if len(batch) >= e.batchSz {
				e.exportLogBatch(batch)
				batch = batch[:0]
				timer.Reset(e.flushInt)
			}

		case <-timer.C:
			if len(batch) > 0 {
				e.exportLogBatch(batch)
				batch = batch[:0]
			}
			timer.Reset(e.flushInt)
		}
	}
}

func (e *Emitter) exportBatch(spans []*tracev1.Span) {
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{{
			Resource: e.resource,
			ScopeSpans: []*tracev1.ScopeSpans{{
				Scope: &commonv1.InstrumentationScope{Name: "workweave-router"},
				Spans: spans,
			}},
		}},
	}

	body, err := proto.Marshal(req)
	if err != nil {
		observability.Get().Warn("Failed to marshal OTLP export request", "err", err)
		return
	}
	e.post(e.endpoint, body)
}

func (e *Emitter) exportLogBatch(records []*logsv1.LogRecord) {
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logsv1.ResourceLogs{{
			Resource: e.resource,
			ScopeLogs: []*logsv1.ScopeLogs{{
				Scope:      &commonv1.InstrumentationScope{Name: "workweave-router"},
				LogRecords: records,
			}},
		}},
	}

	body, err := proto.Marshal(req)
	if err != nil {
		observability.Get().Warn("Failed to marshal OTLP log export request", "err", err)
		return
	}
	e.post(e.logEndpoint, body)
}

func (e *Emitter) post(endpoint string, body []byte) {
	httpReq, err := http.NewRequestWithContext(e.exportCtx, http.MethodPost, endpoint, bytes.NewReader(body))
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		observability.Get().Warn("OTLP export returned non-2xx status", "status", resp.StatusCode)
	}
}
