package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/router/escalationdashboard"
	"weave-os/router/internal/sqlc"
)

// EscalationDashboardRepo projects both retained classifier stores.
type EscalationDashboardRepo struct{ pool *pgxpool.Pool }

// NewEscalationDashboardRepo creates the content-free dashboard reader.
func NewEscalationDashboardRepo(pool *pgxpool.Pool) *EscalationDashboardRepo {
	return &EscalationDashboardRepo{pool: pool}
}

var _ escalationdashboard.Store = (*EscalationDashboardRepo)(nil)

// CreateSnapshot freezes aggregates and newest-first sessions for stable paging.
func (r *EscalationDashboardRepo) CreateSnapshot(ctx context.Context, filter escalationdashboard.Filter) (escalationdashboard.StoredSnapshot, error) {
	queries := sqlc.New(r.pool)
	if err := queries.DeleteExpiredEscalationDashboardSnapshots(ctx, pgtype.Timestamptz{Time: filter.CapturedAt, Valid: true}); err != nil {
		return escalationdashboard.StoredSnapshot{}, fmt.Errorf("delete expired escalation dashboard snapshots: %w", err)
	}
	encoded, err := queries.CreateEscalationDashboardSnapshot(ctx, sqlc.CreateEscalationDashboardSnapshotParams{
		CapturedAt:        pgtype.Timestamptz{Time: filter.CapturedAt, Valid: true},
		Service:           string(filter.Service),
		Mode:              string(filter.Mode),
		OrganizationID:    filter.OrganizationID,
		InstallationID:    filter.InstallationID,
		SessionOutcome:    string(filter.SessionOutcome),
		SnapshotExpiresAt: pgtype.Timestamptz{Time: filter.ExpiresAt, Valid: true},
		PageLimit:         filter.Limit,
	})
	if err != nil {
		return escalationdashboard.StoredSnapshot{}, fmt.Errorf("create escalation dashboard snapshot: %w", err)
	}
	return decodeEscalationDashboardSnapshot(encoded)
}

// SnapshotPage reads one page without recomputing activity or aggregate state.
func (r *EscalationDashboardRepo) SnapshotPage(ctx context.Context, snapshotID string, pageStart, pageLimit int32, readAt time.Time) (escalationdashboard.StoredSnapshot, error) {
	parsedSnapshotID, err := parseUUID(snapshotID)
	if err != nil {
		return escalationdashboard.StoredSnapshot{}, escalationdashboard.ErrInvalidCursor
	}
	encoded, err := sqlc.New(r.pool).GetEscalationDashboardSnapshotPage(ctx, sqlc.GetEscalationDashboardSnapshotPageParams{
		PageStart:  pageStart,
		PageLimit:  pageLimit,
		SnapshotID: parsedSnapshotID,
		ReadAt:     pgtype.Timestamptz{Time: readAt, Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return escalationdashboard.StoredSnapshot{}, escalationdashboard.ErrExpiredCursor
	}
	if err != nil {
		return escalationdashboard.StoredSnapshot{}, fmt.Errorf("query escalation dashboard snapshot page: %w", err)
	}
	return decodeEscalationDashboardSnapshot(encoded)
}

func decodeEscalationDashboardSnapshot(encoded []byte) (escalationdashboard.StoredSnapshot, error) {
	var snapshot escalationdashboard.StoredSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return escalationdashboard.StoredSnapshot{}, fmt.Errorf("decode escalation dashboard: %w", err)
	}
	return snapshot, nil
}
