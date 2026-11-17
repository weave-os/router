package postgres

import (
	"context"
	"database/sql"
	"errors"

	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionstrategy"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
)

type SessionStrategyRepo struct {
	tx sqlc.DBTX
}

func NewSessionStrategyRepo(tx sqlc.DBTX) *SessionStrategyRepo {
	return &SessionStrategyRepo{tx: tx}
}

var _ sessionstrategy.Store = (*SessionStrategyRepo)(nil)

func (r *SessionStrategyRepo) Get(ctx context.Context, installationID uuid.UUID, sessionKey [sessionstrategy.SessionKeyLen]byte) (sessionstrategy.Preference, bool, error) {
	strategy, err := dbbudget.Queries(r.tx).GetSessionStrategyPreference(ctx, sqlc.GetSessionStrategyPreferenceParams{
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

func (r *SessionStrategyRepo) Toggle(ctx context.Context, preference sessionstrategy.Preference) (bool, error) {
	if err := preference.Validate(); err != nil {
		return false, err
	}
	return dbbudget.Queries(r.tx).UpsertToggledSessionStrategyPreference(ctx, sqlc.UpsertToggledSessionStrategyPreferenceParams{
		InstallationID: preference.InstallationID,
		SessionKey:     preference.SessionKey[:],
		Strategy:       string(preference.Strategy),
	})
}

func (r *SessionStrategyRepo) Disable(ctx context.Context, installationID uuid.UUID, sessionKey [sessionstrategy.SessionKeyLen]byte, strategy router.Strategy) (bool, error) {
	disabled, err := dbbudget.Queries(r.tx).UpdateSessionStrategyPreferenceDisabled(ctx, sqlc.UpdateSessionStrategyPreferenceDisabledParams{
		InstallationID: installationID,
		SessionKey:     sessionKey[:],
		Strategy:       string(strategy),
	})
	return disabled > 0, err
}
