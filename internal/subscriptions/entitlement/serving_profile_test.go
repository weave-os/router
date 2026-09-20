package entitlement

import (
	"testing"

	"weave-os/router/internal/router"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServingProfilesCoverEveryPlan(t *testing.T) {
	t.Parallel()

	for _, plan := range []Plan{PlanMax, PlanBoost} {
		profile, ok := ServingProfileFor(plan)
		require.True(t, ok, plan)
		assert.NotEmpty(t, profile.Name)
		assert.Equal(t, router.StrategyHMM, profile.Strategy)
		parsed, err := uuid.Parse(profile.Key)
		require.NoError(t, err)
		assert.NotEqual(t, uuid.Nil, parsed)
	}
}

func TestServingProfileForUnknownPlanFailsClosed(t *testing.T) {
	t.Parallel()

	_, ok := ServingProfileFor(Plan("unknown"))
	assert.False(t, ok)
}
