package postgres

import (
	"context"

	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
	"weave-os/router/internal/telemetry"

	"github.com/google/uuid"
)

var _ telemetry.PolicyShadowStore = (*TelemetryRepo)(nil)

func (r *TelemetryRepo) InsertPolicyShadowDecision(ctx context.Context, p telemetry.PolicyShadowDecision) error {
	id, err := uuid.Parse(p.InstallationID)
	if err != nil {
		return err
	}
	q := dbbudget.Queries(r.tx)
	return q.InsertPolicyShadowDecision(ctx, sqlc.InsertPolicyShadowDecisionParams{
		InstallationID:              id,
		OrganizationID:              stringPtrOrNil(p.OrganizationID),
		RolloutID:                   stringPtrOrNil(p.RolloutID),
		ClientApp:                   stringPtrOrNil(p.ClientApp),
		TrainingAllowed:             p.TrainingAllowed,
		ServingStrategy:             p.ServingStrategy,
		ServingModel:                p.ServingModel,
		ServingProvider:             p.ServingProvider,
		ServingRouteID:              stringPtrOrNil(p.ServingRouteID),
		ServingPolicyRouteKey:       stringPtrOrNil(p.ServingPolicyRouteKey),
		ServingPolicyArtifactID:     stringPtrOrNil(p.ServingPolicyArtifactID),
		ServingPolicyArtifactSha256: stringPtrOrNil(p.ServingPolicyArtifactSHA256),
		ShadowStrategy:              p.ShadowStrategy,
		ShadowModel:                 stringPtrOrNil(p.ShadowModel),
		ShadowProvider:              stringPtrOrNil(p.ShadowProvider),
		ShadowRouteID:               stringPtrOrNil(p.ShadowRouteID),
		ShadowPolicyRouteKey:        stringPtrOrNil(p.ShadowPolicyRouteKey),
		ShadowPolicyArtifactID:      stringPtrOrNil(p.ShadowPolicyArtifactID),
		ShadowPolicyArtifactSha256:  stringPtrOrNil(p.ShadowPolicyArtifactSHA256),
		ShadowLatencyMs:             p.ShadowLatencyMs,
		ShadowError:                 stringPtrOrNil(p.ShadowError),
		ModelsAgree:                 p.ModelsAgree,
	})
}
