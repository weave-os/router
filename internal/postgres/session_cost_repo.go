package postgres

import (
	"context"
	"errors"

	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *TelemetryRepo) GetSessionCost(ctx context.Context, installationID, sessionID string) (turnrecord.SessionCost, error) {
	id, err := uuid.Parse(installationID)
	if err != nil {
		return turnrecord.SessionCost{}, err
	}
	q := dbbudget.Queries(r.tx)
	row, err := q.GetSessionCost(ctx, sqlc.GetSessionCostParams{
		InstallationID: id,
		SessionID:      sessionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return turnrecord.SessionCost{}, turnrecord.ErrSessionCostNotFound
	}
	if err != nil {
		return turnrecord.SessionCost{}, err
	}
	if row.SessionID == nil {
		return turnrecord.SessionCost{}, turnrecord.ErrSessionCostNotFound
	}
	return turnrecord.SessionCost{
		SessionID:              *row.SessionID,
		RequestCount:           row.RequestCount,
		ActualCostUSDMicros:    row.ActualInputCostUsd + row.ActualOutputCostUsd,
		RequestedCostUSDMicros: row.RequestedInputCostUsd + row.RequestedOutputCostUsd,
		InputTokens:            row.InputTokens,
		OutputTokens:           row.OutputTokens,
		CacheCreationTokens:    row.CacheCreationTokens,
		CacheReadTokens:        row.CacheReadTokens,
		LastRecordedAt:         row.LastRecordedAt.Time,
		ContextSnapshot:        turnrecord.ParseContextSnapshot(row.ContextSnapshot),
		BlindExperiment:        row.BlindExperiment,
	}, nil
}
