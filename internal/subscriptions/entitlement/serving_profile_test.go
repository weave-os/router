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

	profile, ok := ServingProfileFor(PlanMax)
	require.True(t, ok)
	assert.NotEmpty(t, profile.Name)
	assert.Equal(t, router.StrategyHMM, profile.Strategy)
	parsed, err := uuid.Parse(profile.Key)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, parsed)

	boost, ok := ServingProfileFor(PlanBoost)
	require.True(t, ok)
	assert.Empty(t, boost)
}

func TestServingProfileForUnknownPlanFailsClosed(t *testing.T) {
	t.Parallel()

	_, ok := ServingProfileFor(Plan("unknown"))
	assert.False(t, ok)
}
