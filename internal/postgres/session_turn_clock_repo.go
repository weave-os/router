package postgres

import (
	"context"
	"encoding/hex"

	"weave-os/router/internal/proxy"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SessionTurnClockRepo struct {
	pool *pgxpool.Pool
}

func NewSessionTurnClockRepo(pool *pgxpool.Pool) *SessionTurnClockRepo {
	return &SessionTurnClockRepo{pool: pool}
}

var _ proxy.SessionTurnClock = (*SessionTurnClockRepo)(nil)

func (r *SessionTurnClockRepo) AdvanceSessionTurnClock(ctx context.Context, advance proxy.SessionTurnClockAdvance) (proxy.SessionTurnClockReading, bool, error) {
	installationID, err := uuid.Parse(advance.InstallationID)
	if err != nil {
		return proxy.SessionTurnClockReading{}, false, err
	}
	var row sqlc.AdvanceSessionTurnClockRow
	err = pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		lockErr := queries.LockSessionTurnClock(ctx, installationID.String()+":"+hex.EncodeToString(advance.SessionKey))
		if lockErr != nil {
			return lockErr
		}
		var advanceErr error
		row, advanceErr = queries.AdvanceSessionTurnClock(ctx, sqlc.AdvanceSessionTurnClockParams{
			InstallationID:  installationID,
			SessionKey:      advance.SessionKey,
			ResponseEndedAt: pgtype.Timestamptz{Time: advance.ResponseEndedAt, Valid: true},
			ServedModel:     advance.ServedModel,
		})
		return advanceErr
	})
	if err != nil {
		return proxy.SessionTurnClockReading{}, false, err
	}
	if !row.PreviousResponseEndedAt.Valid || row.PreviousServedModel == nil {
		return proxy.SessionTurnClockReading{}, false, nil
	}
	return proxy.SessionTurnClockReading{
		ResponseEndedAt: row.PreviousResponseEndedAt.Time,
		ServedModel:     *row.PreviousServedModel,
	}, true, nil
}

func (r *SessionTurnClockRepo) SweepExpired(ctx context.Context) error {
	return sqlc.New(r.pool).SweepStaleSessionTurnClocks(ctx)
}
