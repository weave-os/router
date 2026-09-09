package postgres

import (
	"context"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/sqlc"
)

type blindExperimentRepo struct {
	tx sqlc.DBTX
}

// NewBlindExperimentRepo constructs the read-only experiment assignment repository.
func NewBlindExperimentRepo(tx sqlc.DBTX) auth.BlindExperimentRepository {
	return &blindExperimentRepo{tx: tx}
}

func (repo *blindExperimentRepo) GetForUser(ctx context.Context, installationID, routerUserID string) (auth.BlindExperimentRecord, error) {
	parsedInstallationID, err := parseUUID(installationID)
	if err != nil {
		return auth.BlindExperimentRecord{}, err
	}
	parsedRouterUserID, err := parseUUID(routerUserID)
	if err != nil {
		return auth.BlindExperimentRecord{}, err
	}
	row, err := sqlc.New(repo.tx).GetBlindRouterExperimentForUser(ctx, sqlc.GetBlindRouterExperimentForUserParams{
		RouterUserID:   parsedRouterUserID,
		InstallationID: parsedInstallationID,
	})
	if err != nil {
		return auth.BlindExperimentRecord{}, err
	}
	return auth.BlindExperimentRecord{
		Configured:          row.Configured,
		Enabled:             row.Enabled,
		RouterOnPercentage:  int(row.RouterOnPercentage),
		Seed:                row.Seed,
		CanonicalSubjectKey: derefString(row.CanonicalSubjectKey),
		AutomaticArm:        auth.BlindExperimentArm(derefString(row.AutomaticArm)),
		ManualOverride:      auth.BlindExperimentArm(derefString(row.ManualOverride)),
	}, nil
}
