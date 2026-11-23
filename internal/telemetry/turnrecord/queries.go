package turnrecord

import (
	"context"
	"errors"
	"time"

	"weave-os/router/internal/requestcontext"
)

type Queries struct {
	Repo TelemetryRepository
}

func (q Queries) MetricsSummary(ctx context.Context, installationID string, from, to time.Time) (TelemetrySummary, error) {
	if q.Repo == nil {
		return TelemetrySummary{}, nil
	}
	return q.Repo.GetTelemetrySummary(ctx, installationID, from, to)
}

var (
	ErrInvalidSessionID       = errors.New("session id must be non-empty and at most 128 bytes")
	ErrSessionCostUnavailable = errors.New("router telemetry storage is not configured")
)

func (q Queries) SessionCost(ctx context.Context, installationID, sessionID string) (SessionCost, error) {
	if sessionID == "" || len(sessionID) > requestcontext.MaxClientIdentifierLen {
		return SessionCost{}, ErrInvalidSessionID
	}
	if q.Repo == nil {
		return SessionCost{}, ErrSessionCostUnavailable
	}
	return q.Repo.GetSessionCost(ctx, installationID, sessionID)
}

func (q Queries) MetricsTimeseries(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryBucket, error) {
	if q.Repo == nil {
		return nil, nil
	}
	return q.Repo.GetTelemetryTimeseries(ctx, installationID, from, to, granularity)
}

func (q Queries) MetricsSummaryAll(ctx context.Context, from, to time.Time) (TelemetrySummary, error) {
	if q.Repo == nil {
		return TelemetrySummary{}, nil
	}
	return q.Repo.GetTelemetrySummaryAll(ctx, from, to)
}

func (q Queries) MetricsTimeseriesAll(ctx context.Context, from, to time.Time, granularity string) ([]TelemetryBucket, error) {
	if q.Repo == nil {
		return nil, nil
	}
	return q.Repo.GetTelemetryTimeseriesAll(ctx, from, to, granularity)
}

func (q Queries) MetricsDashboardFacts(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryDashboardFact, error) {
	if q.Repo == nil {
		return nil, nil
	}
	return q.Repo.GetTelemetryDashboardFacts(ctx, installationID, from, to, granularity)
}

func (q Queries) MetricsActiveUsers(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryActiveUsers, error) {
	if q.Repo == nil {
		return nil, nil
	}
	return q.Repo.GetTelemetryDashboardActiveUsers(ctx, installationID, from, to, granularity)
}

func (q Queries) MetricsPeople(ctx context.Context, installationID string) ([]TelemetryPerson, error) {
	if q.Repo == nil {
		return nil, nil
	}
	return q.Repo.GetTelemetryDashboardPeople(ctx, installationID)
}

func (q Queries) MetricsDrillDown(ctx context.Context, installationID string, from, to time.Time) ([]TelemetryDrillDownRow, error) {
	if q.Repo == nil {
		return nil, nil
	}
	return q.Repo.GetTelemetryDashboardDrillDown(ctx, installationID, from, to)
}

func (q Queries) MetricsIndividualFacts(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryIndividualFact, error) {
	if q.Repo == nil {
		return nil, nil
	}
	return q.Repo.GetTelemetryDashboardIndividualFacts(ctx, installationID, from, to, granularity)
}

func (q Queries) MetricsSubscriptionPool(ctx context.Context, installationID string, servedSince, readingsSince time.Time) ([]TelemetrySubscriptionSeat, error) {
	if q.Repo == nil {
		return nil, nil
	}
	return q.Repo.GetTelemetrySubscriptionPoolSeats(ctx, installationID, servedSince, readingsSince)
}

func (q Queries) MetricsModelBreakdown(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]TelemetryModelBucket, error) {
	if q.Repo == nil {
		return nil, nil
	}
	return q.Repo.GetTelemetryModelBreakdown(ctx, installationID, from, to, granularity)
}

func (q Queries) MetricsModelBreakdownAll(ctx context.Context, from, to time.Time, granularity string) ([]TelemetryModelBucket, error) {
	if q.Repo == nil {
		return nil, nil
	}
	return q.Repo.GetTelemetryModelBreakdownAll(ctx, from, to, granularity)
}

func (q Queries) MetricsRows(ctx context.Context, installationID string, from, to time.Time, limit int32) ([]TelemetryRow, error) {
	if q.Repo == nil {
		return nil, nil
	}
	return q.Repo.GetTelemetryRows(ctx, installationID, from, to, limit)
}

func (q Queries) MetricsRowsAll(ctx context.Context, from, to time.Time, limit int32) ([]TelemetryRow, error) {
	if q.Repo == nil {
		return nil, nil
	}
	return q.Repo.GetTelemetryRowsAll(ctx, from, to, limit)
}
