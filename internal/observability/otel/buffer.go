package otel

import (
	"sync"

	"weave-os/router/internal/telemetry"
)

type Buffer struct {
	emitter *Emitter
	traceID [16]byte
	mu      sync.Mutex
	spans   []telemetry.Span
	logs    []telemetry.LogRecord
}

func NewBuffer(emitter *Emitter) *Buffer {
	if emitter == nil {
		return nil
	}
	return &Buffer{
		emitter: emitter,
		traceID: generateTraceID(),
	}
}

func (b *Buffer) Record(s telemetry.Span) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.spans = append(b.spans, s)
	b.mu.Unlock()
}

func (b *Buffer) RecordLog(r telemetry.LogRecord) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.logs = append(b.logs, r)
	b.mu.Unlock()
}

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
