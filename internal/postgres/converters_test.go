package postgres

import (
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToAuthInstallationRoutingQualityWeight(t *testing.T) {
	t.Run("set weight maps to pointer", func(t *testing.T) {
		quality := 0.7
		inst := toAuthInstallation(sqlc.RouterModelRouterInstallation{
			ID:                   uuid.New(),
			ExternalID:           "org-1",
			RoutingQualityWeight: &quality,
		})
		require.NotNil(t, inst.RoutingQualityWeight)
		assert.Equal(t, 0.7, *inst.RoutingQualityWeight)
	})

	t.Run("null weight maps to nil", func(t *testing.T) {
		inst := toAuthInstallation(sqlc.RouterModelRouterInstallation{
			ID:         uuid.New(),
			ExternalID: "org-2",
		})
		assert.Nil(t, inst.RoutingQualityWeight)
	})
}

func TestToAuthInstallationPolicyRouting(t *testing.T) {
	rolloutID := "rollout-1"
	shadow := "future-policy"
	intent := "high"
	strategy := string(router.StrategyHMM)
	inst := toAuthInstallation(sqlc.RouterModelRouterInstallation{
		ID:                           uuid.New(),
		ExternalID:                   "org-1",
		RoutingStrategy:              &strategy,
		RoutingRolloutID:             &rolloutID,
		PolicyShadowStrategy:         &shadow,
		PolicyDebugEnabled:           true,
		PolicyHeaderOverridesEnabled: true,
		PolicyRoutingIntent:          &intent,
		AiTrainingAllowed:            true,
	})

	assert.Equal(t, router.StrategyHMM, inst.RoutingStrategy)
	assert.Equal(t, "rollout-1", inst.RoutingRolloutID)
	assert.Equal(t, router.Strategy("future-policy"), inst.PolicyShadowStrategy)
	assert.True(t, inst.PolicyDebugEnabled)
	assert.True(t, inst.PolicyHeaderOverridesEnabled)
	assert.Equal(t, "high", inst.PolicyRoutingIntent)
	assert.True(t, inst.AITrainingAllowed)
}

func TestToAuthInstallationTrialMode(t *testing.T) {
	enrollment := uuid.New()
	inst := toAuthInstallation(sqlc.RouterModelRouterInstallation{
		ID:                  uuid.New(),
		ExternalID:          "org-trial",
		TrialCaptureEnabled: true,
		TrialEnrollmentID:   pgtype.UUID{Bytes: enrollment, Valid: true},
	})
	assert.True(t, inst.TrialCaptureEnabled)
	assert.Equal(t, enrollment.String(), inst.TrialEnrollmentID)

	unenrolled := toAuthInstallation(sqlc.RouterModelRouterInstallation{ID: uuid.New(), ExternalID: "org-plain"})
	assert.False(t, unenrolled.TrialCaptureEnabled)
	assert.Empty(t, unenrolled.TrialEnrollmentID)
}

func TestToAuthInstallationSubscriptionStateModelsIncludeNewCatalogModels(t *testing.T) {
	inst := toAuthInstallation(sqlc.RouterModelRouterInstallation{
		ID:                                     uuid.New(),
		ExternalID:                             "org-subscription-state",
		SubscriptionStateModelsEnabled:         true,
		ModelsWhenSubscriptionActive:           []string{"gpt-6-luna", "claude-opus-5-5"},
		ModelsWhenSubscriptionInactive:         []string{"gpt-6-luna", "claude-opus-5-5"},
		ModelsExcludedWhenSubscriptionActive:   []string{"claude-opus-5-5"},
		ModelsExcludedWhenSubscriptionInactive: []string{"gpt-6-luna"},
	})

	assert.True(t, inst.SubscriptionStateModelsEnabled)
	assert.Contains(t, inst.ModelsWhenSubscriptionActive, "gpt-6.1-sol")
	assert.Contains(t, inst.ModelsWhenSubscriptionActive, "gpt-6-luna")
	assert.NotContains(t, inst.ModelsWhenSubscriptionActive, "claude-opus-5-5")
	assert.Contains(t, inst.ModelsWhenSubscriptionInactive, "gpt-6.1-sol")
	assert.Contains(t, inst.ModelsWhenSubscriptionInactive, "claude-opus-5-5")
	assert.NotContains(t, inst.ModelsWhenSubscriptionInactive, "gpt-6-luna")
}

func TestToAuthInstallationSubscriptionStateModelsDisabledIgnoresLegacyLists(t *testing.T) {
	inst := toAuthInstallation(sqlc.RouterModelRouterInstallation{
		ID:                             uuid.New(),
		ExternalID:                     "org-subscription-state-off",
		ModelsWhenSubscriptionActive:   []string{"gpt-6-luna"},
		ModelsWhenSubscriptionInactive: []string{"gpt-6-luna"},
	})

	assert.False(t, inst.SubscriptionStateModelsEnabled)
	assert.Empty(t, inst.ModelsWhenSubscriptionActive)
	assert.Empty(t, inst.ModelsWhenSubscriptionInactive)
}
