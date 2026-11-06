package policyregistry_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/subscriptions/entitlement"
)

func TestBoostAdmissionUsesDefaultWithoutRegisteredProfile(t *testing.T) {
	selectionSet := fixtureSet("default-policy")
	activated, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, selectionSet, servingEpoch)
	sets := map[string]policyregistry.SelectionSetView{
		servingRef(t, policyregistry.ServingSelectionSets, selectionSet).SHA256: selectionSet.View(),
	}
	projection := policyregistry.AdmissionProjection{
		Target: policyregistry.TargetStable, Plan: entitlement.PlanBoost,
		EntitlementVersion: 3, AssignmentGeneration: 3,
	}
	admitted, err := policyregistry.SelectSessionRelease(nil, projection, activated, sets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)
	require.Equal(t, selectionSet.Default, admitted.Selection)
	require.Empty(t, admitted.ProfileKey)
	require.Empty(t, admitted.ProfileName)
	require.Equal(t, entitlement.PlanBoost, admitted.Plan)
	require.Equal(t, int64(3), admitted.EntitlementVersion)
}
