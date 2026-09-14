package proxy

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
)

const (
	attemptQueueSize    = 1024
	attemptWriteTimeout = 250 * time.Millisecond
)

// AttemptSink persists diagnostic events in order without blocking dispatch.
// It is not a billing ledger; saturation and failed writes drop events.
// The owner must call Shutdown before closing the store.
type AttemptSink struct {
	store        InferenceAttemptStore
	log          *slog.Logger
	queue        chan attemptRecord
	done         chan struct{}
	cancel       context.CancelFunc
	closeMu      sync.RWMutex
	closed       bool
	dropped      atomic.Uint64
	writeTimeout time.Duration
}

type attemptRecord struct {
	params InsertInferenceAttemptParams
	log    *slog.Logger
}

// NewAttemptSink starts a single ordered persistence worker. Store calls must
// respect context cancellation; a stuck store never blocks RecordAttempt.
func NewAttemptSink(store InferenceAttemptStore, log *slog.Logger) *AttemptSink {
	return newAttemptSink(store, log, attemptQueueSize, attemptWriteTimeout)
}

func newAttemptSink(store InferenceAttemptStore, log *slog.Logger, capacity int, timeout time.Duration) *AttemptSink {
	ctx, cancel := context.WithCancel(context.Background())
	s := &AttemptSink{store: store, log: log, queue: make(chan attemptRecord, capacity), done: make(chan struct{}), cancel: cancel, writeTimeout: timeout}
	go s.run(ctx)
	return s
}

// RecordAttempt snapshots only event values and the request logger, never the
// credential-bearing request context. Enqueue is nonblocking, including shutdown.
func (s *AttemptSink) RecordAttempt(ctx context.Context, event inference.AttemptEvent) {
	if s.store == nil {
		return
	}
	installationID, _ := ctx.Value(InstallationIDContextKey{}).(string)
	if installationID == "" {
		return
	}
	record := attemptRecord{params: InsertInferenceAttemptParams{InstallationID: installationID, Event: event}, log: observability.FromContext(ctx)}
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed {
		s.dropped.Add(1)
		return
	}
	select {
	case s.queue <- record:
	default:
		s.dropped.Add(1)
	}
}

// Dropped reports events lost to saturation, failed writes or shutdown.
func (s *AttemptSink) Dropped() uint64 { return s.dropped.Load() }

// Shutdown stops enqueueing and drains until ctx expires, then cancels the
// in-flight write and discards pending events. Concurrent calls are safe.
func (s *AttemptSink) Shutdown(ctx context.Context) error {
	s.closeMu.Lock()
	if !s.closed {
		s.closed = true
		close(s.queue)
	}
	s.closeMu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		s.cancel()
		return ctx.Err()
	}
}

func (s *AttemptSink) run(ctx context.Context) {
	defer close(s.done)
	defer s.cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			s.dropped.Add(1)
			s.log.Error("Attempt persistence worker panicked", "panic", recovered)
			s.closeMu.Lock()
			if !s.closed {
				s.closed = true
				close(s.queue)
			}
			s.closeMu.Unlock()
			for range s.queue {
				s.dropped.Add(1)
			}
		}
		if dropped := s.Dropped(); dropped > 0 {
			s.log.Warn("Inference attempt events dropped", "count", dropped)
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var reported uint64
	for {
		var record attemptRecord
		select {
		case <-ticker.C:
			if dropped := s.Dropped(); dropped != reported {
				s.log.Warn("Inference attempt events dropped", "count", dropped)
				reported = dropped
			}
			continue
		case queued, ok := <-s.queue:
			if !ok {
				return
			}
			record = queued
		}
		if ctx.Err() != nil {
			s.dropped.Add(1)
			continue
		}
		writeCtx, cancel := context.WithTimeout(ctx, s.writeTimeout)
		err := s.store.InsertInferenceAttempt(writeCtx, record.params)
		cancel()
		if err != nil {
			s.dropped.Add(1)
			record.log.Warn("Inference attempt persistence failed", "operation_id", record.params.Event.OperationID, "attempt_index", record.params.Event.AttemptIndex, "err", err)
		}
	}
}
