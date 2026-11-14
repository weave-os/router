package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/router/switchpolicy"
	"weave-os/router/internal/sqlc"
)

var _ switchpolicy.ShadowStore = (*TelemetryRepo)(nil)

func (r *TelemetryRepo) GetAgreementShadow(ctx context.Context, key switchpolicy.ShadowKey) (switchpolicy.ShadowSnapshot, bool, error) {
	id, err := uuid.Parse(key.InstallationID)
	if err != nil {
		return switchpolicy.ShadowSnapshot{}, false, err
	}
	row, err := dbbudget.Queries(r.tx).GetAgreementShadowState(ctx, sqlc.GetAgreementShadowStateParams{InstallationID: id, SessionKey: key.SessionKey, Role: key.Role})
	if errors.Is(err, sql.ErrNoRows) {
		return switchpolicy.ShadowSnapshot{}, false, nil
	}
	if err != nil {
		return switchpolicy.ShadowSnapshot{}, false, err
	}
	var state switchpolicy.ShadowSnapshot
	if err := json.Unmarshal(row.State, &state); err != nil {
		return state, false, err
	}
	state.Revision = row.Revision
	return state, true, nil
}

func (r *TelemetryRepo) CompareAndSwapAgreementShadow(ctx context.Context, key switchpolicy.ShadowKey, expected int64, next switchpolicy.ShadowSnapshot) (bool, error) {
	id, err := uuid.Parse(key.InstallationID)
	if err != nil {
		return false, err
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	q := dbbudget.Queries(r.tx)
	var affected int64
	if expected == 0 {
		affected, err = q.CreateAgreementShadowState(ctx, sqlc.CreateAgreementShadowStateParams{InstallationID: id, SessionKey: key.SessionKey, Role: key.Role, State: raw})
	} else {
		affected, err = q.UpdateAgreementShadowState(ctx, sqlc.UpdateAgreementShadowStateParams{InstallationID: id, SessionKey: key.SessionKey, Role: key.Role, State: raw, ExpectedRevision: expected})
	}
	return affected == 1, err
}
