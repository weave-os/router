package policyregistry_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

func TestCurrentPolicySourceTracksActivationAndProfile(t *testing.T) {
	store, _, selectionSet := controllerFixture(t)
	release := store.object(t, policyregistry.ServingReleases, selectionSet.Default.Release).(*policyregistry.ServingRelease)
	selectionSet.Profiles[profileKeyOne] = registerProfileFixture(t, store, selectionSet.Default, profileKeyOne, release.Policy)
	active, _ := storedActivateFixture(t, store, policyregistry.ServingStateSnapshot{}, selectionSet, servingEpoch)
	store.states[policyregistry.TargetStable] = active
	source := policyregistry.CurrentPolicySource{Store: store}

	defaultPolicy, err := source.ReadCurrentPolicy(context.Background(), policyregistry.TargetStable, "")
	require.NoError(t, err)
	profilePolicy, err := source.ReadCurrentPolicy(context.Background(), policyregistry.TargetStable, profileKeyOne)
	require.NoError(t, err)
	assert.Equal(t, active.State.CurrentActivationID, defaultPolicy.ActivationID)
	assert.Equal(t, profileKeyOne, profilePolicy.ProfileKey)
	assert.NotEmpty(t, profilePolicy.PolicySHA256)
	assert.NotEmpty(t, profilePolicy.Roster.Clusters)

	_, err = source.ReadCurrentPolicy(context.Background(), policyregistry.TargetStable, profileKeyTwo)
	require.ErrorIs(t, err, policyregistry.ErrNoActivePolicy)
	_, err = source.ReadCurrentPolicy(context.Background(), policyregistry.TargetStaging, "")
	require.Error(t, err)

	replacement, _ := storedActivateFixture(t, store, active, variantSet(t, store, selectionSet, "worker-0002"), servingEpoch.Add(time.Minute))
	store.states[policyregistry.TargetStable] = replacement
	currentPolicy, err := source.ReadCurrentPolicy(context.Background(), policyregistry.TargetStable, "")
	require.NoError(t, err)
	assert.Equal(t, replacement.State.CurrentActivationID, currentPolicy.ActivationID)
	assert.NotEqual(t, defaultPolicy.ActivationID, currentPolicy.ActivationID)
	assert.NotEqual(t, defaultPolicy.SelectionSetSHA256, currentPolicy.SelectionSetSHA256)
}
