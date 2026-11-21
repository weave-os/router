package admin_test

import (
	"context"
	"time"

	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/google/uuid"
)

// stubTelemetryRepo satisfies turnrecord.TelemetryRepository with no-ops so a test
// can embed it and override only the method under test.
type stubTelemetryRepo struct{}

func (stubTelemetryRepo) InsertRequestTelemetry(context.Context, turnrecord.InsertTelemetryParams) error {
	return nil
}

func (stubTelemetryRepo) GetTelemetrySummary(context.Context, string, time.Time, time.Time) (turnrecord.TelemetrySummary, error) {
	return turnrecord.TelemetrySummary{}, nil
}

func (stubTelemetryRepo) GetTelemetryTimeseries(context.Context, string, time.Time, time.Time, string) ([]turnrecord.TelemetryBucket, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetrySummaryAll(context.Context, time.Time, time.Time) (turnrecord.TelemetrySummary, error) {
	return turnrecord.TelemetrySummary{}, nil
}

func (stubTelemetryRepo) GetTelemetryTimeseriesAll(context.Context, time.Time, time.Time, string) ([]turnrecord.TelemetryBucket, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryRows(context.Context, string, time.Time, time.Time, int32) ([]turnrecord.TelemetryRow, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryRowsAll(context.Context, time.Time, time.Time, int32) ([]turnrecord.TelemetryRow, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryModelBreakdown(context.Context, string, time.Time, time.Time, string) ([]turnrecord.TelemetryModelBucket, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryModelBreakdownAll(context.Context, time.Time, time.Time, string) ([]turnrecord.TelemetryModelBucket, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryBySessionSequence(context.Context, uuid.UUID, []byte, string, int) (turnrecord.TelemetryTurnResult, error) {
	return turnrecord.TelemetryTurnResult{}, nil
}

func (stubTelemetryRepo) GetSessionCost(context.Context, string, string) (turnrecord.SessionCost, error) {
	return turnrecord.SessionCost{}, turnrecord.ErrSessionCostNotFound
}

func (stubTelemetryRepo) GetTelemetryDashboardFacts(context.Context, string, time.Time, time.Time, string) ([]turnrecord.TelemetryDashboardFact, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryDashboardActiveUsers(context.Context, string, time.Time, time.Time, string) ([]turnrecord.TelemetryActiveUsers, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryDashboardPeople(context.Context, string) ([]turnrecord.TelemetryPerson, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetrySubscriptionPoolSeats(context.Context, string, time.Time, time.Time) ([]turnrecord.TelemetrySubscriptionSeat, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryDashboardDrillDown(context.Context, string, time.Time, time.Time) ([]turnrecord.TelemetryDrillDownRow, error) {
	return nil, nil
}

func (stubTelemetryRepo) GetTelemetryDashboardIndividualFacts(context.Context, string, time.Time, time.Time, string) ([]turnrecord.TelemetryIndividualFact, error) {
	return nil, nil
}
