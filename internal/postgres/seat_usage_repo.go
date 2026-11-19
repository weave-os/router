package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/sqlc"
)

type SeatUsageRepo struct{ tx sqlc.DBTX }

func NewSeatUsageRepo(tx sqlc.DBTX) *SeatUsageRepo {
	return &SeatUsageRepo{tx: tx}
}

func (r *SeatUsageRepo) RecordSeatUsage(ctx context.Context, routerUserID, provider string, snapshot usage.Snapshot) error {
	userID, err := uuid.Parse(routerUserID)
	if err != nil {
		return err
	}
	return dbbudget.Queries(r.tx).UpsertRouterUserSeatUsage(ctx, sqlc.UpsertRouterUserSeatUsageParams{
		RouterUserID:           userID,
		Provider:               provider,
		PrimaryUsedPercent:     snapshot.Primary.UsedPercent,
		PrimaryWindowMinutes:   int64(snapshot.Primary.WindowMinutes),
		PrimaryResetsAt:        optionalTimestamptz(snapshot.Primary.ResetAt),
		SecondaryUsedPercent:   snapshot.Secondary.UsedPercent,
		SecondaryWindowMinutes: int64(snapshot.Secondary.WindowMinutes),
		SecondaryResetsAt:      optionalTimestamptz(snapshot.Secondary.ResetAt),
		OverageInUse:           snapshot.OverageInUse,
		ObservedAt:             pgtype.Timestamptz{Time: snapshot.ObservedAt.UTC(), Valid: true},
	})
}

func (r *SeatUsageRepo) ListSeatUsage(ctx context.Context, routerUserID string) ([]usage.SeatUsage, error) {
	userID, err := uuid.Parse(routerUserID)
	if err != nil {
		return nil, err
	}
	rows, err := dbbudget.Queries(r.tx).ListRouterUserSeatUsage(ctx, userID)
	if err != nil {
		return nil, err
	}
	seats := make([]usage.SeatUsage, 0, len(rows))
	for _, row := range rows {
		seats = append(seats, usage.SeatUsage{Provider: row.Provider, Snapshot: usage.Snapshot{
			Primary:      usage.Window{UsedPercent: row.PrimaryUsedPercent, WindowMinutes: int(row.PrimaryWindowMinutes), ResetAt: row.PrimaryResetsAt.Time},
			Secondary:    usage.Window{UsedPercent: row.SecondaryUsedPercent, WindowMinutes: int(row.SecondaryWindowMinutes), ResetAt: row.SecondaryResetsAt.Time},
			OverageInUse: row.OverageInUse,
			ObservedAt:   row.ObservedAt.Time,
		}})
	}
	return seats, nil
}

func optionalTimestamptz(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}
