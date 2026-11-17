package postgres

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
)

type LLMEscalationCheckFixture struct {
	queries        *sqlc.Queries
	installationID uuid.UUID
}

func NewLLMEscalationCheckFixture(pool *pgxpool.Pool, installationID string) (*LLMEscalationCheckFixture, error) {
	id, err := uuid.Parse(installationID)
	if err != nil {
		return nil, err
	}
	return &LLMEscalationCheckFixture{queries: dbbudget.Queries(pool), installationID: id}, nil
}

func (f *LLMEscalationCheckFixture) ExpireJob(ctx context.Context, jobID string) error {
	id, err := uuid.Parse(jobID)
	if err != nil {
		return err
	}
	return f.queries.UpdateLLMEscalationFixtureLeaseExpired(ctx, sqlc.UpdateLLMEscalationFixtureLeaseExpiredParams{InstallationID: f.installationID, ID: id})
}

func (f *LLMEscalationCheckFixture) ExpireSession(ctx context.Context, lifetime string) error {
	id, err := uuid.Parse(lifetime)
	if err != nil {
		return err
	}
	return f.queries.UpdateLLMEscalationFixtureSessionExpired(ctx, sqlc.UpdateLLMEscalationFixtureSessionExpiredParams{InstallationID: f.installationID, Lifetime: id})
}
