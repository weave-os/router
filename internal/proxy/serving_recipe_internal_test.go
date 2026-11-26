package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recipeServingCtx(t *testing.T) context.Context {
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
	return flags.WithOverrides(ctx, overrides)
}

func TestServedTurnRecipeRecordsWorkerClassifierAndScoringPolicy(t *testing.T) {
	ctx := recipeServingCtx(t)
	const overrideDigest = "b0761e1fb7237aa27bdb4044fa856bd5f0c7c71d28985972af43ea06b9733883"
	telemetry := turnrecord.InsertTelemetryParams{Strategy: string(router.StrategyHMMEmbedding)}
	served := router.Decision{Metadata: &router.RoutingMetadata{SelectionPolicySHA256: "historical-policy-sha"}}

	applyServedTurnTelemetry(ctx, hmm.ServingPorts(), &telemetry, routingpolicy.TurnRoute{Decision: served}, served, dispatch.EffortResolution{})

	assert.Equal(t, "prod/stable", telemetry.ServingTarget)
	assert.Equal(t, turnrecord.ServingRecipe{
		ServingRecipe: requestcontext.ServingRecipe{
			WorkerRevision:            "router-worker-00042",
			WorkerImageDigest:         "sha256:worker",
			WorkerConfigurationSHA256: "configuration-sha",
			PolicySHA256:              "historical-policy-sha",
			ClassifierReleaseID:       "classifier-release-sha",
			ClassifierEnabledVariants: "primary",
		},
		EffectiveStrategy:   string(router.StrategyHMMEmbedding),
		FlagOverridesSHA256: overrideDigest,
	}, telemetry.ServingRecipe)
}

func TestUnscoredTurnRecipeFallsBackToAdmittedPolicy(t *testing.T) {
	ctx := recipeServingCtx(t)
	var telemetry turnrecord.InsertTelemetryParams

	applyServedTurnTelemetry(ctx, hmm.ServingPorts(), &telemetry, routingpolicy.TurnRoute{}, router.Decision{}, dispatch.EffortResolution{})

	assert.Equal(t, "admitted-policy-sha", telemetry.ServingRecipe.PolicySHA256)
	assert.Equal(t, "router-worker-00042", telemetry.ServingRecipe.WorkerRevision)
	assert.Equal(t, string(router.StrategyHMMEmbedding), telemetry.ServingRecipe.EffectiveStrategy, "an unscored turn records the resolved request strategy")
}

func TestPassthroughTurnRecipeRecordsTheResolvedStrategy(t *testing.T) {
	ctx := recipeServingCtx(t)
	ctx = router.WithStrategySelection(ctx, router.StrategySelection{Strategy: router.StrategyHMMBeta})
	var telemetry turnrecord.InsertTelemetryParams

	applySubscriberTelemetry(ctx, &telemetry)

	assert.Equal(t, string(router.StrategyHMMBeta), telemetry.ServingRecipe.EffectiveStrategy)
}

func TestUnmanagedTurnRecordsNoRecipe(t *testing.T) {
	telemetry := turnrecord.InsertTelemetryParams{Strategy: string(router.StrategyHMMEmbedding)}

	served := router.Decision{Metadata: &router.RoutingMetadata{SelectionPolicySHA256: "policy-sha"}}
	applyServedTurnTelemetry(context.Background(), hmm.ServingPorts(), &telemetry, routingpolicy.TurnRoute{Decision: served}, served, dispatch.EffortResolution{})

	assert.Equal(t, turnrecord.ServingRecipe{}, telemetry.ServingRecipe)
}
