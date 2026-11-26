package serving

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/router"
	"weave-os/router/internal/sqlc"
)

type RoutingModelProfileRepo struct{ queries *sqlc.Queries }

func NewRoutingModelProfileRepo(pool *pgxpool.Pool) *RoutingModelProfileRepo {
	return &RoutingModelProfileRepo{queries: dbbudget.Queries(pool)}
}

func (r *RoutingModelProfileRepo) ProfileKey(ctx context.Context, installationID string, model router.RoutingModel) (string, bool, error) {
	installationUUID, err := uuid.Parse(installationID)
	if err != nil {
		return "", false, err
	}
	assignment, err := r.queries.GetServingRoutingModelProfileAssignment(ctx, sqlc.GetServingRoutingModelProfileAssignmentParams{InstallationID: installationUUID, RoutingModel: string(model)})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return assignment.ProfileKey.String(), true, nil
}
