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

// Toggle flips the explicit beta preference in one statement and returns the
// state now persisted. Disabled sessions keep a row and use stable routing.
func (r *SessionStrategyRepo) Toggle(ctx context.Context, preference sessionstrategy.Preference) (bool, error) {
	if err := preference.Validate(); err != nil {
		return false, err
	}
	return sqlc.New(r.tx).UpsertToggledSessionStrategyPreference(ctx, sqlc.UpsertToggledSessionStrategyPreferenceParams{
		InstallationID: preference.InstallationID,
		SessionKey:     preference.SessionKey[:],
		Strategy:       string(preference.Strategy),
	})
}

// Disable turns the explicit beta preference off in one statement and reports
// whether it had been enabled.
func (r *SessionStrategyRepo) Disable(ctx context.Context, installationID uuid.UUID, sessionKey [sessionstrategy.SessionKeyLen]byte) (bool, error) {
	disabled, err := sqlc.New(r.tx).UpdateSessionStrategyPreferenceDisabled(ctx, sqlc.UpdateSessionStrategyPreferenceDisabledParams{
		InstallationID: installationID,
		SessionKey:     sessionKey[:],
	})
	return disabled > 0, err
}
