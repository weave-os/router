package hmmstub

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServingGraphPinsComposeSelectionSet(t *testing.T) {
	graph, err := NewServingGraph("http://hmm-stub:8093")
	require.NoError(t, err)
	require.Equal(t, ServingSelectionSetSHA256, graph.SelectionSet.SHA256)
	require.Equal(t, registryRoot+"/artifacts/"+ServingSelectionSetSHA256+".json", graph.SelectionSet.URI)
	for _, startup := range []string{"../../docker-compose.yml", "../../scripts/smoke/runner.py"} {
		contents, err := os.ReadFile(startup)
		require.NoError(t, err)
		require.Contains(t, string(contents), graph.SelectionSet.URI, startup)
		require.Contains(t, string(contents), ServingConfiguration().URI, startup)
		require.Contains(t, string(contents), ServingConfiguration().SHA256, startup)
	}
}
