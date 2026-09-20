package serving

import (
	"testing"

	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriberPlanProjectionUsesServerOwnedProfile(t *testing.T) {
	t.Parallel()

	for _, plan := range []entitlement.Plan{entitlement.PlanMax, entitlement.PlanBoost} {
		t.Run(string(plan), func(t *testing.T) {
			profile, ok := entitlement.ServingProfileFor(plan)
			require.True(t, ok)

			projection, err := subscriberPlanProjection(string(plan), 7)
			require.NoError(t, err)
			assert.Equal(t, profile.Key, projection.ProfileKey)
			assert.Equal(t, profile.Name, projection.ProfileName)
			assert.Equal(t, plan, projection.Plan)
			assert.Equal(t, int64(7), projection.EntitlementVersion)
			assert.Equal(t, int64(7), projection.AssignmentGeneration)
		})
	}
}

func TestSubscriberPlanProjectionRejectsUnknownOrUnversionedPlan(t *testing.T) {
	t.Parallel()

	_, err := subscriberPlanProjection("unknown", 1)
	require.Error(t, err)
	_, err = subscriberPlanProjection(string(entitlement.PlanMax), 0)
	require.Error(t, err)
}
