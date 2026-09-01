package admin_test

import (
	"context"
	"time"

	"workweave/router/internal/proxy"

	"github.com/google/uuid"
)

// stubTelemetryRepo satisfies proxy.TelemetryRepository with no-ops so a test
// can embed it and override only the method under test.
type stubTelemetryRepo struct{}

func (stubTelemetryRepo) InsertRequestTelemetry(context.Context, proxy.InsertTelemetryParams) error {
	return nil
}

func (stubTelemetryRepo) GetTelemetrySummary(context.Context, string, time.Time, time.Time) (proxy.TelemetrySummary, error) {
	return proxy.TelemetrySummary{}, nil
}

func (stubTelemetryRepo) GetTelemetryTimeseries(context.Context, string, time.Time, time.Time, string) ([]proxy.TelemetryBucket, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetrySummaryAll(context.Context, time.Time, time.Time) (proxy.TelemetrySummary, error) {
	return proxy.TelemetrySummary{}, nil
}

func (stubTelemetryRepo) GetTelemetryTimeseriesAll(context.Context, time.Time, time.Time, string) ([]proxy.TelemetryBucket, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryRows(context.Context, string, time.Time, time.Time, int32) ([]proxy.TelemetryRow, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryRowsAll(context.Context, time.Time, time.Time, int32) ([]proxy.TelemetryRow, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryModelBreakdown(context.Context, string, time.Time, time.Time, string) ([]proxy.TelemetryModelBucket, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryModelBreakdownAll(context.Context, time.Time, time.Time, string) ([]proxy.TelemetryModelBucket, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryBySessionSequence(context.Context, uuid.UUID, []byte, string, int) (proxy.TelemetryTurnResult, error) {
	return proxy.TelemetryTurnResult{}, nil
}

func (stubTelemetryRepo) GetSessionCost(context.Context, string, string) (proxy.SessionCost, error) {
	return proxy.SessionCost{}, proxy.ErrSessionCostNotFound
}
