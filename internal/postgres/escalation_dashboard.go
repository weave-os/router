package postgres

import (
	"context"
	"encoding/json"
	"fmt"

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

// Snapshot returns aggregates and one page captured against the same expiry cutoff.
func (r *EscalationDashboardRepo) Snapshot(ctx context.Context, filter escalationdashboard.Filter) (escalationdashboard.Snapshot, error) {
	encoded, err := sqlc.New(r.pool).GetEscalationDashboard(ctx, sqlc.GetEscalationDashboardParams{
		CapturedAt:     pgtype.Timestamptz{Time: filter.CapturedAt, Valid: true},
		Service:        string(filter.Service),
		Mode:           string(filter.Mode),
		OrganizationID: filter.OrganizationID,
		InstallationID: filter.InstallationID,
		SessionOutcome: string(filter.SessionOutcome),
		PageLimit:      filter.Limit,
		PageOffset:     filter.Offset,
	})
	if err != nil {
		return escalationdashboard.Snapshot{}, fmt.Errorf("query escalation dashboard: %w", err)
	}
	var snapshot escalationdashboard.Snapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return escalationdashboard.Snapshot{}, fmt.Errorf("decode escalation dashboard: %w", err)
	}
	return snapshot, nil
}
