package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EscalationCheckFixture confines integration-only expiry and cleanup to the
// disposable installation created by the database verification command.
type EscalationCheckFixture struct {
	queries        *sqlc.Queries
	installationID uuid.UUID
	externalID     string
	scope          [32]byte
}

// NewEscalationCheckFixture binds destructive fixture operations to one installation.
func NewEscalationCheckFixture(pool *pgxpool.Pool, installation auth.Installation, scope [32]byte) *EscalationCheckFixture {
	return &EscalationCheckFixture{queries: sqlc.New(pool), installationID: uuid.MustParse(installation.ID), externalID: installation.ExternalID, scope: scope}
}

// Cleanup deletes the fixture and verifies its session children were cascaded.
func (f *EscalationCheckFixture) Cleanup(ctx context.Context) error {
	deleted, err := f.queries.DeleteEscalationCheckFixture(ctx, sqlc.DeleteEscalationCheckFixtureParams{InstallationID: f.installationID, ExternalID: f.externalID})
	if err != nil {
		return fmt.Errorf("delete escalation check fixture: %w", err)
	}
	if deleted != 1 {
		return fmt.Errorf("fixture cleanup deleted %d installations", deleted)
	}
	remaining, err := f.queries.GetEscalationCheckFixtureRemaining(ctx, sqlc.GetEscalationCheckFixtureRemainingParams{InstallationID: f.installationID, Scope: f.scope[:]})
	if err != nil {
		return fmt.Errorf("verify escalation check cleanup: %w", err)
	}
	if remaining != 0 {
		return fmt.Errorf("fixture cleanup left %d rows", remaining)
	}
	return nil
}

// CheckExpiryBoundary expires the fixture between the production delete and
// claim statements, then verifies the upsert cannot resurrect its old lifetime.
func (f *EscalationCheckFixture) CheckExpiryBoundary(ctx context.Context, boundary [32]byte) error {
	err := f.queries.DeleteExpiredEscalationSession(ctx, f.scope[:])
	if err != nil {
		return err
	}
	expired, err := f.queries.UpdateEscalationCheckFixtureExpired(ctx, sqlc.UpdateEscalationCheckFixtureExpiredParams{Scope: f.scope[:], InstallationID: f.installationID, ExternalID: f.externalID})
	if err != nil {
		return fmt.Errorf("expire escalation fixture: %w", err)
	}
	if expired != 1 {
		return fmt.Errorf("expire escalation fixture: rows=%d", expired)
	}
	_, err = f.queries.UpsertEscalationSessionClaim(ctx, sqlc.UpsertEscalationSessionClaimParams{Scope: f.scope[:], InstallationID: f.installationID, LeaseToken: uuid.New(), Boundary: boundary[:]})
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("claim resurrected an expired lifetime: %v", err)
	}
	return nil
}
