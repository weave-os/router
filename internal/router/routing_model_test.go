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
