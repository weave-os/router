package postgres

import (
	"context"
	"database/sql"
	"errors"

	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionstrategy"
	"workweave/router/internal/sqlc"

	"github.com/google/uuid"
)

// SessionStrategyRepo adapts sessionstrategy.Store to SQLC-generated queries.
type SessionStrategyRepo struct {
	tx sqlc.DBTX
}

// NewSessionStrategyRepo wires the adapter over a pgx pool or transaction.
func NewSessionStrategyRepo(tx sqlc.DBTX) *SessionStrategyRepo {
	return &SessionStrategyRepo{tx: tx}
}

var _ sessionstrategy.Store = (*SessionStrategyRepo)(nil)

// Get reads the explicit beta preference. A missing row means stable routing.
func (r *SessionStrategyRepo) Get(ctx context.Context, installationID uuid.UUID, sessionKey [sessionstrategy.SessionKeyLen]byte) (sessionstrategy.Preference, bool, error) {
	strategy, err := sqlc.New(r.tx).GetSessionStrategyPreference(ctx, sqlc.GetSessionStrategyPreferenceParams{
		InstallationID: installationID,
		SessionKey:     sessionKey[:],
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sessionstrategy.Preference{}, false, nil
		}
		return sessionstrategy.Preference{}, false, err
	}
	return sessionstrategy.Preference{
		InstallationID: installationID,
		SessionKey:     sessionKey,
		Strategy:       router.Strategy(strategy),
	}, true, nil
}

// Set records the explicit beta preference.
func (r *SessionStrategyRepo) Set(ctx context.Context, preference sessionstrategy.Preference) error {
	if err := preference.Validate(); err != nil {
		return err
	}
	return sqlc.New(r.tx).UpsertSessionStrategyPreference(ctx, sqlc.UpsertSessionStrategyPreferenceParams{
		InstallationID: preference.InstallationID,
		SessionKey:     preference.SessionKey[:],
		Strategy:       string(preference.Strategy),
	})
}

// Clear removes the explicit preference so the session uses stable routing.
func (r *SessionStrategyRepo) Clear(ctx context.Context, installationID uuid.UUID, sessionKey [sessionstrategy.SessionKeyLen]byte) error {
	return sqlc.New(r.tx).DeleteSessionStrategyPreference(ctx, sqlc.DeleteSessionStrategyPreferenceParams{
		InstallationID: installationID,
		SessionKey:     sessionKey[:],
	})
}
