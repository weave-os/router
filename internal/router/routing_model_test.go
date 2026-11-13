package router

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutingModelIDsUseVersionedSnapshotNames(t *testing.T) {
	versionedID := regexp.MustCompile(`^weave-(hmm|qwen)-[0-9]{8}-[0-9]{4}v[0-9]+$`)
	require.Equal(t, RoutingModel("weave-auto"), ModelAutomatic)
	require.Regexp(t, versionedID, string(ModelHMM))
	require.Regexp(t, versionedID, string(ModelQwen))
	require.NotEqual(t, RoutingModel("weave-hmm"), ModelHMM)
	require.NotEqual(t, RoutingModel("weave-qwen"), ModelQwen)
}
