package postgres

import (
	"context"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
)

type routingPolicyRepo struct{ tx sqlc.DBTX }

// NewRoutingPolicyRepo constructs read-only generic installation policy access.
func NewRoutingPolicyRepo(tx sqlc.DBTX) auth.RoutingPolicyRepository {
	return &routingPolicyRepo{tx: tx}
}

func (repo *routingPolicyRepo) GetPolicy(ctx context.Context, installationID string) (auth.RoutingPolicy, error) {
	parsedInstallationID, err := parseUUID(installationID)
	if err != nil {
		return auth.RoutingPolicy{}, err
	}
	row, err := dbbudget.Queries(repo.tx).GetInstallationRoutingPolicy(ctx, parsedInstallationID)
	if err != nil {
		return auth.RoutingPolicy{}, err
	}
	return auth.RoutingPolicy{Mode: auth.RoutingPolicyMode(row.Mode), Revision: row.Revision, ReportingExperimentID: row.ReportingExperimentID}, nil
}

func (repo *routingPolicyRepo) HasAssignment(ctx context.Context, installationID, routerUserID string, revision int64) (bool, error) {
	parsedInstallationID, err := parseUUID(installationID)
	if err != nil {
		return false, err
	}
	parsedUserID, err := parseUUID(routerUserID)
	if err != nil {
		return false, err
	}
	return dbbudget.Queries(repo.tx).HasInstallationRoutingAssignment(ctx, sqlc.HasInstallationRoutingAssignmentParams{
		InstallationID: parsedInstallationID, RouterUserID: parsedUserID, Revision: revision,
	})
}
