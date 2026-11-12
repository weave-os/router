package otel

import (
	"sync"

	"weave-os/router/internal/telemetry"
)

// Buffer accumulates spans for a single request and flushes them to an Emitter
// in bulk. All methods are nil-safe (zero overhead when disabled).
type Buffer struct {
	emitter *Emitter
	traceID [16]byte
	mu      sync.Mutex
	spans   []telemetry.Span
	logs    []telemetry.LogRecord
}

// NewBuffer creates a request-scoped span buffer. Returns nil when emitter is
// nil (OTel disabled).
func NewBuffer(emitter *Emitter) *Buffer {
	if emitter == nil {
		return nil
	}
	return &Buffer{
		emitter: emitter,
		traceID: generateTraceID(),
	}
}

// Record appends a span to the buffer. Safe for concurrent use.
func (b *Buffer) Record(s telemetry.Span) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.spans = append(b.spans, s)
	b.mu.Unlock()
}

// RecordLog appends a log record to the buffer. Safe for concurrent use.
func (b *Buffer) RecordLog(r telemetry.LogRecord) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.logs = append(b.logs, r)
	b.mu.Unlock()
}

// Flush drains all buffered spans and log records into the emitter's async
// export queues. Spans and logs share this request's traceID.
func (b *Buffer) Flush() {
	if b == nil {
		return
	}

	b.mu.Lock()
	pendingSpans := b.spans
	b.spans = nil
	pendingLogs := b.logs
	b.logs = nil
	b.mu.Unlock()

	for _, s := range pendingSpans {
		b.emitter.Enqueue(spanToProto(s, b.traceID))
	}
	for _, r := range pendingLogs {
		b.emitter.EnqueueLog(logRecordToProto(r, b.traceID))
	}
}
