package postgres

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/sqlc"
)

// LLMEscalationCheckFixture confines expiry injection to one disposable installation.
type LLMEscalationCheckFixture struct {
	queries        *sqlc.Queries
	installationID uuid.UUID
}

// NewLLMEscalationCheckFixture binds the runtime check to its created installation.
func NewLLMEscalationCheckFixture(pool *pgxpool.Pool, installationID string) (*LLMEscalationCheckFixture, error) {
	id, err := uuid.Parse(installationID)
	if err != nil {
		return nil, err
	}
	return &LLMEscalationCheckFixture{queries: sqlc.New(pool), installationID: id}, nil
}

// ExpireJob makes a single job's lease stale without waiting for wall-clock expiry.
func (f *LLMEscalationCheckFixture) ExpireJob(ctx context.Context, jobID string) error {
	id, err := uuid.Parse(jobID)
	if err != nil {
		return err
	}
	return f.queries.UpdateLLMEscalationFixtureLeaseExpired(ctx, sqlc.UpdateLLMEscalationFixtureLeaseExpiredParams{InstallationID: f.installationID, ID: id})
}

// ExpireSession advances only the fixture lifetime to its retention boundary.
func (f *LLMEscalationCheckFixture) ExpireSession(ctx context.Context, lifetime string) error {
	id, err := uuid.Parse(lifetime)
	if err != nil {
		return err
	}
	return f.queries.UpdateLLMEscalationFixtureSessionExpired(ctx, sqlc.UpdateLLMEscalationFixtureSessionExpiredParams{InstallationID: f.installationID, Lifetime: id})
}
