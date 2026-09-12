package proxy

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/observability"

	"github.com/google/uuid"
)

// panicTelemetryRepo is a TelemetryRepository whose InsertRequestTelemetry
// always panics, simulating a bug in the telemetry sink.
type panicTelemetryRepo struct{}

func (panicTelemetryRepo) InsertRequestTelemetry(ctx context.Context, p InsertTelemetryParams) error {
	panic("boom: telemetry insert")
}

func (panicTelemetryRepo) GetTelemetrySummary(ctx context.Context, installationID string, from, to time.Time) (TelemetrySummary, error) {
	return TelemetrySummary{}, nil
}

func (panicTelemetryRepo) GetTelemetryTimeseries(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryBucket, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetrySummaryAll(ctx context.Context, from, to time.Time) (TelemetrySummary, error) {
	return TelemetrySummary{}, nil
}

func (panicTelemetryRepo) GetTelemetryTimeseriesAll(ctx context.Context, from, to time.Time, granularity string) ([]TelemetryBucket, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetryRows(ctx context.Context, installationID string, from, to time.Time, limit int32) ([]TelemetryRow, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetryRowsAll(ctx context.Context, from, to time.Time, limit int32) ([]TelemetryRow, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetryModelBreakdown(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryModelBucket, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetTelemetryModelBreakdownAll(ctx context.Context, from, to time.Time, granularity string) ([]TelemetryModelBucket, error) {
	return nil, nil
}

func (panicTelemetryRepo) GetSessionCost(ctx context.Context, installationID, sessionID string) (SessionCost, error) {
	return SessionCost{}, ErrSessionCostNotFound
}

func (panicTelemetryRepo) GetTelemetryBySessionSequence(ctx context.Context, installationID uuid.UUID, sessionKey []byte, role string, seq int) (TelemetryTurnResult, error) {
	return TelemetryTurnResult{}, nil
}

func TestFireTelemetryRecoversFromPanic(t *testing.T) {
	workers := testObservationWorkers(t)
	s := &Service{telemetry: panicTelemetryRepo{}, observations: workers}
	s.fireTelemetry(context.Background(), InsertTelemetryParams{RequestID: "req-1"})
	completed := make(chan struct{})
	workers.Database.Submit(observability.WorkTelemetry, nil, time.Second, observability.FromContext(context.Background()), func(context.Context, []byte) error { close(completed); return nil })
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("telemetry panic removed the DB worker")
	}
}
