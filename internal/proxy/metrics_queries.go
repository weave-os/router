package proxy

import (
	"context"
	"errors"
	"time"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/telemetry/turnrecord"
)

func (s *Service) MetricsSummary(ctx context.Context, installationID string, from, to time.Time) (turnrecord.TelemetrySummary, error) {
	if s.turnTelemetry.repo == nil {
		return turnrecord.TelemetrySummary{}, nil
	}
	return s.turnTelemetry.repo.GetTelemetrySummary(ctx, installationID, from, to)
}

var (
	ErrInvalidSessionID       = errors.New("session id must be non-empty and at most 128 bytes")
	ErrSessionCostUnavailable = errors.New("router telemetry storage is not configured")
)

func (s *Service) SessionCost(ctx context.Context, installationID, sessionID string) (turnrecord.SessionCost, error) {
	if sessionID == "" || len(sessionID) > requestcontext.MaxClientIdentifierLen {
		return turnrecord.SessionCost{}, ErrInvalidSessionID
	}
	if s.turnTelemetry.repo == nil {
		return turnrecord.SessionCost{}, ErrSessionCostUnavailable
	}
	return s.turnTelemetry.repo.GetSessionCost(ctx, installationID, sessionID)
}

func (s *Service) MetricsTimeseries(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]turnrecord.TelemetryBucket, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryTimeseries(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsSummaryAll(ctx context.Context, from, to time.Time) (turnrecord.TelemetrySummary, error) {
	if s.turnTelemetry.repo == nil {
		return turnrecord.TelemetrySummary{}, nil
	}
	return s.turnTelemetry.repo.GetTelemetrySummaryAll(ctx, from, to)
}

func (s *Service) MetricsTimeseriesAll(ctx context.Context, from, to time.Time, granularity string) ([]turnrecord.TelemetryBucket, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryTimeseriesAll(ctx, from, to, granularity)
}

func (s *Service) MetricsDashboardFacts(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]turnrecord.TelemetryDashboardFact, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryDashboardFacts(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsActiveUsers(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]turnrecord.TelemetryActiveUsers, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryDashboardActiveUsers(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsPeople(ctx context.Context, installationID string) ([]turnrecord.TelemetryPerson, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryDashboardPeople(ctx, installationID)
}

func (s *Service) MetricsDrillDown(ctx context.Context, installationID string, from, to time.Time) ([]turnrecord.TelemetryDrillDownRow, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryDashboardDrillDown(ctx, installationID, from, to)
}

func (s *Service) MetricsIndividualFacts(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]turnrecord.TelemetryIndividualFact, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryDashboardIndividualFacts(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsSubscriptionPool(ctx context.Context, installationID string, servedSince, readingsSince time.Time) ([]turnrecord.TelemetrySubscriptionSeat, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetrySubscriptionPoolSeats(ctx, installationID, servedSince, readingsSince)
}

func (s *Service) MetricsModelBreakdown(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]turnrecord.TelemetryModelBucket, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryModelBreakdown(ctx, installationID, from, to, granularity)
}

func (s *Service) MetricsModelBreakdownAll(ctx context.Context, from, to time.Time, granularity string) ([]turnrecord.TelemetryModelBucket, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryModelBreakdownAll(ctx, from, to, granularity)
}

func (s *Service) MetricsRows(ctx context.Context, installationID string, from, to time.Time, limit int32) ([]turnrecord.TelemetryRow, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryRows(ctx, installationID, from, to, limit)
}

func (s *Service) MetricsRowsAll(ctx context.Context, from, to time.Time, limit int32) ([]turnrecord.TelemetryRow, error) {
	if s.turnTelemetry.repo == nil {
		return nil, nil
	}
	return s.turnTelemetry.repo.GetTelemetryRowsAll(ctx, from, to, limit)
}
