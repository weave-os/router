package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recipeServingCtx(t *testing.T) (context.Context, flags.Overrides) {
	t.Helper()
	overrides, err := flags.ParseOverrides([]byte(`{"turn_signal_capture_enabled":false}`))
	require.NoError(t, err)
	ctx := requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{
		Target:    "prod/stable",
		ReleaseID: "release-sha",
		Recipe: requestcontext.ServingRecipe{
			WorkerRevision:            "router-worker-00042",
			WorkerImageDigest:         "sha256:worker",
			WorkerConfigurationSHA256: "configuration-sha",
			PolicySHA256:              "admitted-policy-sha",
			ClassifierReleaseID:       "classifier-release-sha",
			ClassifierEnabledVariants: "primary",
		},
	})
	return flags.WithOverrides(ctx, overrides), overrides
}

func TestServedTurnRecipeRecordsWorkerClassifierAndScoringPolicy(t *testing.T) {
	ctx, overrides := recipeServingCtx(t)
	telemetry := turnrecord.InsertTelemetryParams{Strategy: string(router.StrategyHMMEmbedding)}
	served := router.Decision{Metadata: &router.RoutingMetadata{SelectionPolicySHA256: "historical-policy-sha"}}

	applyServedPolicyTelemetry(ctx, &telemetry, served)
	applySubscriberTelemetry(ctx, &telemetry)

	assert.Equal(t, "prod/stable", telemetry.ServingTarget)
	assert.Equal(t, turnrecord.ServingRecipe{
		WorkerRevision:            "router-worker-00042",
		WorkerImageDigest:         "sha256:worker",
		WorkerConfigurationSHA256: "configuration-sha",
		PolicySHA256:              "historical-policy-sha",
		EffectiveStrategy:         string(router.StrategyHMMEmbedding),
		ClassifierReleaseID:       "classifier-release-sha",
		ClassifierEnabledVariants: "primary",
		FlagOverridesSHA256:       overrides.Digest(),
	}, telemetry.ServingRecipe)
}

func TestUnscoredTurnRecipeFallsBackToAdmittedPolicy(t *testing.T) {
	ctx, _ := recipeServingCtx(t)
	var telemetry turnrecord.InsertTelemetryParams

	applyServedPolicyTelemetry(ctx, &telemetry, router.Decision{})
	applySubscriberTelemetry(ctx, &telemetry)

	assert.Equal(t, "admitted-policy-sha", telemetry.ServingRecipe.PolicySHA256)
	assert.Equal(t, "router-worker-00042", telemetry.ServingRecipe.WorkerRevision)
}

func TestUnmanagedTurnRecordsNoRecipe(t *testing.T) {
	telemetry := turnrecord.InsertTelemetryParams{Strategy: string(router.StrategyHMMEmbedding)}

	applyServedPolicyTelemetry(context.Background(), &telemetry, router.Decision{Metadata: &router.RoutingMetadata{SelectionPolicySHA256: "policy-sha"}})
	applySubscriberTelemetry(context.Background(), &telemetry)

	assert.Equal(t, turnrecord.ServingRecipe{}, telemetry.ServingRecipe)
}
