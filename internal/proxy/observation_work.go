package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"weave-os/router/internal/observability"
)

// WithObservationWorkers injects process-owned asynchronous capacity. Without it,
// optional persistence/reporting is disabled; no per-service workers are started.
func (s *Service) WithObservationWorkers(workers *observability.ObservationWorkers) *Service {
	s.observations = workers
	return s
}

// submitObservation snapshots the DTO before returning to the caller. Every
// input is already bounded at its source (capped response capture, request body
// limits, metadata-sized rows), so the snapshot is never size-gated: a job that
// cannot be represented is the only rejection. Decoding runs in fixed workers.
func submitObservation[T any](queue *observability.WorkQueue, kind observability.WorkKind, log *slog.Logger, payload T, timeout time.Duration, persist func(context.Context, T) error) {
	if queue == nil {
		return
	}
	snapshot, err := json.Marshal(payload)
	if err != nil {
		queue.Reject(kind, log, err)
		return
	}
	queue.Submit(kind, snapshot, timeout, log, func(ctx context.Context, body []byte) error {
		var decoded T
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		err := decoder.Decode(&decoded)
		if err != nil {
			return err
		}
		return persist(ctx, decoded)
	})
}
