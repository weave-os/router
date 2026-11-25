package router

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutingModelIDsUseVersionedSnapshotNames(t *testing.T) {
	versionedID := regexp.MustCompile(`^weave-(hmm|qwen)-[0-9]{8}-[0-9]{4}v[0-9]+$`)
	require.Equal(t, ModelAutomatic, RoutingModel("weave-auto"))
	require.Regexp(t, versionedID, string(ModelHMM))
	require.Regexp(t, versionedID, string(ModelQwen))
	require.NotEqual(t, ModelHMM, RoutingModel("weave-hmm"))
	require.NotEqual(t, ModelQwen, RoutingModel("weave-qwen"))
}

func TestAutomaticRoutingModelAcceptsDisplayName(t *testing.T) {
	require.True(t, IsRoutingModel("weave-auto"))
	require.True(t, IsRoutingModel("Weave Auto"))
	require.Equal(t, ModelAutomatic, CanonicalRoutingModel("Weave Auto"))
	require.Equal(t, ModelAutomatic, CanonicalRoutingModel("weave-auto"))
	for _, near := range []string{"weave auto", "WEAVE AUTO", "Weave  Auto", " Weave Auto", "Weave Auto ", "Weave-Auto", "Weave Router"} {
		require.False(t, IsRoutingModel(near), near)
		require.Equal(t, RoutingModel(near), CanonicalRoutingModel(near), near)
	}
}

func TestAutoEfficientIsAnAutomaticRoutingModel(t *testing.T) {
	require.True(t, IsRoutingModel("weave-auto-efficient"))
	require.True(t, UsesAssignedProfile(ModelAutoEfficient))
	require.False(t, UsesAssignedProfile(ModelAutomatic))
	require.False(t, IsRoutingModel("weave-auto-efficiency"))
	require.Equal(t, "claude-sonnet-5-5", AutoEfficientComparisonModel)
}
