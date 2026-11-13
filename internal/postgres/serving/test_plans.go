package serving

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
)

// TestPlanRepo reads eligible subjects and stores grant digests on the primary database.
type TestPlanRepo struct{ queries *sqlc.Queries }

// NewTestPlanRepo binds test authorization to budgeted primary queries.
func NewTestPlanRepo(pool *pgxpool.Pool) *TestPlanRepo {
	return &TestPlanRepo{queries: dbbudget.Queries(pool)}
}

// ListTestIdentities lists enrolled personal identities with funded enabled budgets.
func (r *TestPlanRepo) ListTestIdentities(ctx context.Context) ([]policyregistry.TestPlanIdentity, error) {
	rows, err := r.queries.GetInternalTestIdentities(ctx)
	if err != nil {
		return nil, err
	}
	identities := make([]policyregistry.TestPlanIdentity, 0, len(rows))
	for _, row := range rows {
		identities = append(identities, policyregistry.TestPlanIdentity{SubjectID: row.SubjectID.String(), InstallationID: row.InstallationID.String(), Label: row.Label, BalanceMicros: row.BalanceUsdMicros, EnrollmentGeneration: row.EnrollmentGeneration})
	}
	return identities, nil
}

// GetTestIdentity reads current eligibility and funding for a subject.
func (r *TestPlanRepo) GetTestIdentity(ctx context.Context, subjectID string) (policyregistry.TestPlanIdentity, error) {
	subject, err := uuid.Parse(subjectID)
	if err != nil {
		return policyregistry.TestPlanIdentity{}, err
	}
	row, err := r.queries.GetInternalTestIdentity(ctx, subject)
	if err != nil {
		return policyregistry.TestPlanIdentity{}, err
	}
	return policyregistry.TestPlanIdentity{SubjectID: row.SubjectID.String(), InstallationID: row.InstallationID.String(), Label: row.Label, BalanceMicros: row.BalanceUsdMicros, EnrollmentGeneration: row.EnrollmentGeneration}, nil
}

// SaveTestLaunch persists the exact preview and grant digest without the bearer token.
func (r *TestPlanRepo) SaveTestLaunch(ctx context.Context, launch policyregistry.TestPlanLaunch, hash string) error {
	encoded, err := json.Marshal(launch)
	if err != nil {
		return err
	}
	return r.queries.InsertInternalTestLaunch(ctx, sqlc.InsertInternalTestLaunchParams{ID: uuid.MustParse(launch.ID), TokenSha256: hash, SubjectID: uuid.MustParse(launch.Preview.Identity.SubjectID), InstallationID: uuid.MustParse(launch.Preview.Identity.InstallationID), Launch: encoded, CreatedAt: pgtype.Timestamptz{Time: launch.CreatedAt, Valid: true}, ExpiresAt: pgtype.Timestamptz{Time: launch.ExpiresAt, Valid: true}})
}

// AuthorizeTestLaunch checks key ownership and binds a grant to one session atomically.
func (r *TestPlanRepo) AuthorizeTestLaunch(ctx context.Context, hash, installationID, keyID, sessionID string) (policyregistry.TestPlanLaunch, error) {
	installation, err := uuid.Parse(installationID)
	if err != nil {
		return policyregistry.TestPlanLaunch{}, err
	}
	key, err := uuid.Parse(keyID)
	if err != nil {
		return policyregistry.TestPlanLaunch{}, err
	}
	session, err := uuid.Parse(sessionID)
	if err != nil {
		return policyregistry.TestPlanLaunch{}, err
	}
	encoded, err := r.queries.UpdateInternalTestLaunchSession(ctx, sqlc.UpdateInternalTestLaunchSessionParams{TokenSha256: hash, InstallationID: installation, APIKeyID: key, SessionID: session})
	if err != nil {
		return policyregistry.TestPlanLaunch{}, errors.New("test grant is invalid, expired, revoked or already bound to another session")
	}
	var launch policyregistry.TestPlanLaunch
	err = json.Unmarshal(encoded, &launch)
	return launch, err
}

// RevokeTestLaunch stops subsequent admissions without changing entitlements.
func (r *TestPlanRepo) RevokeTestLaunch(ctx context.Context, id string) error {
	launchID, err := uuid.Parse(id)
	if err != nil {
		return err
	}
	updated, err := r.queries.UpdateInternalTestLaunchRevoked(ctx, launchID)
	if err != nil {
		return err
	}
	if updated == 0 {
		return policyregistry.ErrTestLaunchNotFound
	}
	return nil
}
