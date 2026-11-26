package postgres

import (
	"weave-os/router/internal/sqlc"
	"weave-os/router/internal/telemetry/turnrecord"
)

func applyServingTelemetryParams(params *sqlc.InsertRequestTelemetryParams, p turnrecord.InsertTelemetryParams) {
	params.ServingProfileID = stringPtrOrNil(p.ServingProfileID)
	params.ServingProfileVersion = stringPtrOrNil(p.ServingProfileVersion)
	params.ServingReleaseID = stringPtrOrNil(p.ServingReleaseID)
	params.ServingBindingID = stringPtrOrNil(p.ServingBindingID)
	params.ServingTarget = stringPtrOrNil(p.ServingTarget)
	params.WorkerRevision = stringPtrOrNil(p.ServingRecipe.WorkerRevision)
	params.WorkerImageDigest = stringPtrOrNil(p.ServingRecipe.WorkerImageDigest)
	params.WorkerConfigurationSha256 = stringPtrOrNil(p.ServingRecipe.WorkerConfigurationSHA256)
	params.ServingPolicySha256 = stringPtrOrNil(p.ServingRecipe.PolicySHA256)
	params.EffectiveStrategy = stringPtrOrNil(p.ServingRecipe.EffectiveStrategy)
	params.ClassifierReleaseID = stringPtrOrNil(p.ServingRecipe.ClassifierReleaseID)
	params.ClassifierEnabledVariants = stringPtrOrNil(p.ServingRecipe.ClassifierEnabledVariants)
	params.FlagOverridesSha256 = stringPtrOrNil(p.ServingRecipe.FlagOverridesSHA256)
}
