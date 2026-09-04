package postgres

import (
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
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

func TestToAuthInstallationSubscriptionConditionalModels(t *testing.T) {
	inst := toAuthInstallation(sqlc.RouterModelRouterInstallation{
		ID:                             uuid.New(),
		ExternalID:                     "org-conditional",
		ModelsWhenSubscriptionActive:   []string{"claude-sonnet-5"},
		ModelsWhenSubscriptionInactive: []string{"gpt-5.6-terra"},
	})

	assert.Equal(t, []string{"claude-sonnet-5"}, inst.ModelsWhenSubscriptionActive)
	assert.Equal(t, []string{"gpt-5.6-terra"}, inst.ModelsWhenSubscriptionInactive)
}
