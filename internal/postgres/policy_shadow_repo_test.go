package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/postgres"
	"weave-os/router/internal/telemetry"
)

func TestPolicyShadowDecisionStoresRecipeShadowColumns(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	installationID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO router.model_router_installations (id, external_id, name) VALUES ($1, $2, $2)`,
		installationID, installationID.String()[:30])
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM router.model_router_installations WHERE id = $1", installationID)
	})

	err = postgres.NewTelemetryRepo(pool).InsertPolicyShadowDecision(ctx, telemetry.PolicyShadowDecision{
		InstallationID: installationID.String(), ServingStrategy: "hmm", ServingModel: "claude-opus-5-5", ServingProvider: "anthropic",
		ShadowStrategy: "hmm", ShadowModel: "claude-sonnet-5-5", ShadowProvider: "anthropic",
		ShadowRoutingModel: "weave-auto-efficient", ShadowProfileKey: "00000000-0000-5000-8000-0000000000e1",
		ClassifierPredictedLabel: "high", ServingPolicyGroup: "high", ShadowPolicyGroup: "high",
		ServingRosterArmID: "anthropic/claude-opus-5.5", ShadowRosterArmID: "anthropic/claude-sonnet-5.5",
	})
	require.NoError(t, err)

	var shadowRoutingModel, shadowProfileKey, predictedLabel, shadowArm string
	var servingProfileKey *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT shadow_routing_model, shadow_profile_key, serving_profile_key, classifier_predicted_label, shadow_roster_arm_id
		 FROM router.policy_shadow_decisions WHERE installation_id = $1`, installationID).
		Scan(&shadowRoutingModel, &shadowProfileKey, &servingProfileKey, &predictedLabel, &shadowArm))
	assert.Equal(t, "weave-auto-efficient", shadowRoutingModel)
	assert.Equal(t, "00000000-0000-5000-8000-0000000000e1", shadowProfileKey)
	assert.Nil(t, servingProfileKey, "the default profile is stored as NULL")
	assert.Equal(t, "high", predictedLabel)
	assert.Equal(t, "anthropic/claude-sonnet-5.5", shadowArm)
}
