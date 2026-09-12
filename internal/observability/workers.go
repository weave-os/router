package observability

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// WorkKind identifies optional work without introducing per-request metric labels.
type WorkKind string

const (
	WorkAttempt   WorkKind = "inference_attempt"
	WorkTelemetry WorkKind = "request_telemetry"
	WorkOutcome   WorkKind = "policy_outcome"
	WorkFeedback  WorkKind = "policy_feedback"
	// MaxWorkPayloadBytes bounds a serialized job before ownership transfer.
	MaxWorkPayloadBytes = 1 << 20
	workQueueSize       = 256
	workQueueBytes      = 16 << 20
	workLogInterval     = 10 * time.Second
)

type workLane string

const (
	workDatabase workLane = "database"
	workRemote   workLane = "remote"
)

type workResult string

const (
	workInvalidPayload workResult = "invalid_payload"
	workOversize       workResult = "oversize"
	workClosing        workResult = "closing"
	workFailed         workResult = "failed"
	workPanicked       workResult = "panicked"
)

type observationJob struct {
	kind    WorkKind
	payload []byte
	timeout time.Duration
	log     *slog.Logger
	run     func(context.Context, []byte) error
}

// WorkQueue owns a fixed number of workers. Count and byte reservations include
// running jobs; callers must capture only long-lived sinks in run, never requests.
type WorkQueue struct {
	mu                sync.Mutex
	capacityAvailable *sync.Cond
	queue             chan observationJob
	closed            bool
	count             int
	retainedBytes     int
	lastLog           time.Time
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
	counter           metric.Int64Counter
	lane              workLane
}

// ObservationWorkers isolates database observations from slow remote reports.
// Construct once before serving traffic, and shut down before closing sinks.
type ObservationWorkers struct {
	Database *WorkQueue
	Remote   *WorkQueue
}

// NewObservationWorkers starts one database and two remote workers.
func NewObservationWorkers() *ObservationWorkers {
	return &ObservationWorkers{Database: newWorkQueue(workDatabase, 1), Remote: newWorkQueue(workRemote, 2)}
}

func newWorkQueue(lane workLane, workers int) *WorkQueue {
	ctx, cancel := context.WithCancel(context.Background())
	counter, _ := otel.Meter("weave-os/router/observations").Int64Counter("router.observations.dropped")
	q := &WorkQueue{queue: make(chan observationJob, workQueueSize), ctx: ctx, cancel: cancel, done: make(chan struct{}), counter: counter, lane: lane}
	q.capacityAvailable = sync.NewCond(&q.mu)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() { defer wg.Done(); q.worker() }()
	}
	go func() { wg.Wait(); close(q.done) }()
	return q
}

// Submit copies payload on admission, waiting for capacity when saturated.
// run receives a shutdown-owned context with timeout, not a request context.
// Nil queues disable observations; oversized payloads and closing queues reject.
// Jobs must not submit back into their own full queue.
func (q *WorkQueue) Submit(kind WorkKind, payload []byte, timeout time.Duration, log *slog.Logger, run func(context.Context, []byte) error) bool {
	if q == nil {
		return false
	}
	if len(payload) > MaxWorkPayloadBytes {
		q.record(log, kind, workOversize, nil)
		return false
	}
	q.mu.Lock()
	for !q.closed && (q.count >= workQueueSize || len(payload) > workQueueBytes-q.retainedBytes) {
		q.capacityAvailable.Wait()
	}
	if q.closed {
		q.mu.Unlock()
		q.record(log, kind, workClosing, nil)
		return false
	}
	q.count++
	q.retainedBytes += len(payload)
	// count includes running jobs, so this send always has a free slot.
	q.queue <- observationJob{kind: kind, payload: bytes.Clone(payload), timeout: timeout, log: log, run: run}
	q.mu.Unlock()
	return true
}

// Reject records a payload that could not be prepared within the admission bound.
func (q *WorkQueue) Reject(kind WorkKind, log *slog.Logger, err error) {
	if q != nil {
		q.record(log, kind, workInvalidPayload, err)
	}
}

func (q *WorkQueue) worker() {
	for job := range q.queue {
		if q.ctx.Err() != nil {
			q.record(job.log, job.kind, workClosing, nil)
		} else {
			q.execute(job)
		}
		q.mu.Lock()
		q.count--
		q.retainedBytes -= len(job.payload)
		job = observationJob{}
		q.capacityAvailable.Broadcast()
		q.mu.Unlock()
	}
}

func (q *WorkQueue) execute(job observationJob) {
	defer func() {
		if recover() != nil {
			q.record(job.log, job.kind, workPanicked, nil)
		}
	}()
	ctx, cancel := context.WithTimeout(q.ctx, job.timeout)
	defer cancel()
	err := job.run(ctx, job.payload)
	if err != nil {
		q.record(job.log, job.kind, workFailed, err)
	}
}

func (q *WorkQueue) record(log *slog.Logger, kind WorkKind, reason workResult, err error) {
	if q.counter != nil {
		q.counter.Add(context.Background(), 1, metric.WithAttributes(attribute.String("lane", string(q.lane)), attribute.String("kind", string(kind)), attribute.String("reason", string(reason))))
	}
	q.mu.Lock()
	emit := time.Since(q.lastLog) >= workLogInterval
	if emit {
		q.lastLog = time.Now()
	}
	q.mu.Unlock()
	if emit {
		log.Error("Optional observation dropped", "work_kind", kind, "work_lane", q.lane, "reason", reason, "err", err)
	}
}

// Shutdown stops admissions, wakes blocked producers, and drains within ctx.
// Expiry cancels active I/O and drops queued jobs. Repeated callers share completion.
func (q *WorkQueue) Shutdown(ctx context.Context) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.queue)
		q.capacityAvailable.Broadcast()
	}
	q.mu.Unlock()
	select {
	case <-q.done:
		q.cancel()
		return nil
	case <-ctx.Done():
		q.cancel()
		return ctx.Err()
	}
}

// Shutdown drains both lanes concurrently against one deadline.
func (w *ObservationWorkers) Shutdown(ctx context.Context) error {
	if w == nil {
		return nil
	}
	results := make(chan error, 2)
	go func() { results <- w.Database.Shutdown(ctx) }()
	go func() { results <- w.Remote.Shutdown(ctx) }()
	first := <-results
	second := <-results
	if first != nil {
		return first
	}
	return second
}
