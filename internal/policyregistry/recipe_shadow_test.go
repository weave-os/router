package policyregistry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

func TestRecipePolicySourceReadsTheProfileFromTheCurrentActivation(t *testing.T) {
	store, controller, set := controllerFixture(t)
	base := *mustAs[*policyregistry.ServingRelease](t, store.object(t, policyregistry.ServingReleases, set.Default.Release))
	set.Profiles[profileKeyOne] = registerProfileFixture(t, store, set.Default, profileKeyOne, base.Policy)
	store.publish(t, policyregistry.ServingSelectionSets, set)
	proposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	_, err := controller.Activate(context.Background(), store.publishArtifact(t, policyregistry.ServingProposal, proposal), "workflow")
	require.NoError(t, err)
	source, err := policyregistry.NewRecipePolicySource(store, set.Target)
	require.NoError(t, err)

	recipe, err := source.ProfilePolicy(context.Background(), profileKeyOne)

	require.NoError(t, err)
	assert.Equal(t, profileKeyOne, recipe.ProfileKey)
	assert.Equal(t, base.Policy.SHA256, recipe.Policy.SHA256)
	require.NotNil(t, recipe.Roster)

	store.readErr = errors.New("registry unavailable")
	cached, err := source.ProfilePolicy(context.Background(), profileKeyOne)
	require.NoError(t, err, "a loaded profile is served from cache without another registry read")
	assert.Same(t, recipe.Roster, cached.Roster)
	store.readErr = nil

	_, err = source.ProfilePolicy(context.Background(), profileKeyTwo)
	require.ErrorIs(t, err, policyregistry.ErrRecipeProfileNotActive, "a profile outside the active selection set is never shadowed")
}

func TestRecipePolicySourceRequiresStoreAndTarget(t *testing.T) {
	_, err := policyregistry.NewRecipePolicySource(nil, policyregistry.TargetStable)
	require.Error(t, err)
	store, _, _ := controllerFixture(t)
	_, err = policyregistry.NewRecipePolicySource(store, "")
	require.Error(t, err)
}
