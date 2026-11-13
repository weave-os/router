package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
)

type blindExperimentSessionRepo struct {
	tx sqlc.DBTX
}

// NewBlindExperimentSessionRepo constructs the session-mode experiment repository.
func NewBlindExperimentSessionRepo(tx sqlc.DBTX) auth.BlindExperimentSessionRepository {
	return &blindExperimentSessionRepo{tx: tx}
}

func (repo *blindExperimentSessionRepo) GetForInstallation(ctx context.Context, installationID string) (auth.BlindExperimentRecord, error) {
	parsedInstallationID, err := parseUUID(installationID)
	if err != nil {
		return auth.BlindExperimentRecord{}, err
	}
	row, err := dbbudget.Queries(repo.tx).GetBlindRouterExperimentForInstallation(ctx, parsedInstallationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.BlindExperimentRecord{}, nil
	}
	if err != nil {
		return auth.BlindExperimentRecord{}, err
	}
	return auth.BlindExperimentRecord{
		Configured:         true,
		Enabled:            row.Enabled,
		RouterOnPercentage: int(row.RouterOnPercentage),
		Seed:               row.Seed,
		SessionMode:        row.SessionMode,
		CohortExperimentID: row.CohortExperimentID,
	}, nil
}

func (repo *blindExperimentSessionRepo) ClaimSessionArm(ctx context.Context, installationID, sessionSubjectKey string, arm auth.BlindExperimentArm) (auth.BlindExperimentArm, error) {
	parsedInstallationID, err := parseUUID(installationID)
	if err != nil {
		return "", err
	}
	claimed, err := dbbudget.Queries(repo.tx).ClaimBlindExperimentSessionArm(ctx, sqlc.ClaimBlindExperimentSessionArmParams{
		InstallationID: parsedInstallationID,
		SessionKey:     sessionSubjectKey,
		Arm:            string(arm),
	})
	if err != nil {
		return "", err
	}
	return auth.BlindExperimentArm(claimed), nil
}
