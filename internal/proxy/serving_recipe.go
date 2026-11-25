package proxy

import (
	"context"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry/turnrecord"
)

func applyServingWorkerTelemetry(ctx context.Context, identity requestcontext.ServingIdentity, telemetry *turnrecord.InsertTelemetryParams) {
	telemetry.ServingRecipe.WorkerRevision = identity.Recipe.WorkerRevision
	telemetry.ServingRecipe.WorkerImageDigest = identity.Recipe.WorkerImageDigest
	telemetry.ServingRecipe.WorkerConfigurationSHA256 = identity.Recipe.WorkerConfigurationSHA256
	telemetry.ServingRecipe.ClassifierReleaseID = identity.Recipe.ClassifierReleaseID
	telemetry.ServingRecipe.ClassifierEnabledVariants = identity.Recipe.ClassifierEnabledVariants
	telemetry.ServingRecipe.EffectiveStrategy = telemetry.Strategy
	if telemetry.ServingRecipe.PolicySHA256 == "" {
		telemetry.ServingRecipe.PolicySHA256 = identity.Recipe.PolicySHA256
	}
	if overrides, found := flags.OverridesFromContext(ctx); found {
		telemetry.ServingRecipe.FlagOverridesSHA256 = overrides.Digest()
	}
}

func applyServedPolicyTelemetry(ctx context.Context, telemetry *turnrecord.InsertTelemetryParams, served router.Decision) {
	if _, managed := requestcontext.ServingIdentityFromContext(ctx); !managed || served.Metadata == nil {
		return
	}
	telemetry.ServingRecipe.PolicySHA256 = served.Metadata.SelectionPolicySHA256
}
