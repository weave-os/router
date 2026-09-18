package serving_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres/serving"
	"weave-os/router/internal/sqlc"
)

type feedbackDB struct {
	sqlc.DBTX
	row  feedbackRow
	args []any
}

func (db *feedbackDB) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	db.args = args
	return db.row
}

type feedbackRow struct {
	request        string
	scope, binding []byte
	err            error
}

func (row feedbackRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	*dest[0].(*string) = row.request
	*dest[1].(*uuid.UUID) = uuid.New()
	*dest[2].(*[]byte) = row.scope
	*dest[3].(*[]byte) = row.binding
	return nil
}

func TestFeedbackLookupConstrainsInstallationAndPreservesTuple(t *testing.T) {
	installation := uuid.New()
	scope, err := json.Marshal(policyregistry.AdmissionScope{InstallationID: installation.String(), CredentialIdentity: "subject"})
	require.NoError(t, err)
	want := policyregistry.SessionReleaseBinding{Target: policyregistry.TargetInternal, ActivationID: "old-activation", BindingGeneration: 7, ProfileKey: "customer-profile"}
	binding, err := json.Marshal(want)
	require.NoError(t, err)
	db := &feedbackDB{row: feedbackRow{request: "request", scope: scope, binding: binding}}
	lookup := serving.FeedbackLookup{Queries: sqlc.New(db)}
	admission, err := lookup.GetFeedbackAdmission(context.Background(), installation.String(), "request")
	require.NoError(t, err)
	assert.Equal(t, want, admission)
	assert.Equal(t, []any{"request", installation}, db.args)
	_, err = lookup.GetFeedbackAdmission(context.Background(), uuid.NewString(), "request")
	require.ErrorContains(t, err, "scope differs")
	_, err = lookup.GetFeedbackAdmission(context.Background(), installation.String(), "different-request")
	require.ErrorContains(t, err, "scope differs")
	db.row.err = sql.ErrNoRows
	_, err = lookup.GetFeedbackAdmission(context.Background(), installation.String(), "request")
	require.ErrorIs(t, err, sql.ErrNoRows)
}
