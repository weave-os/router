package catalog

import (
	"testing"

	"weave-os/router/internal/router"

	"github.com/stretchr/testify/require"
)

func TestModelChoicesDescribeRoutersWithoutAddingDispatchModels(t *testing.T) {
	choices := ModelChoices()
	require.Len(t, choices, len(Listing())+4)
	seen := make(map[string]bool)
	for _, choice := range choices {
		require.NotEmpty(t, choice.Description)
		require.False(t, seen[choice.Model], "duplicate model ID %s", choice.Model)
		seen[choice.Model] = true
		_, isDispatchable := ByID(choice.Model)
		if choice.Kind == ModelChoiceRouter {
			require.False(t, isDispatchable)
			require.True(t, router.IsRoutingModel(choice.Model))
		} else {
			require.Equal(t, ModelChoiceDirect, choice.Kind)
			require.True(t, isDispatchable)
		}
	}
	require.True(t, seen[string(router.ModelHMM)])
	require.True(t, seen[string(router.ModelQwen)])
	require.True(t, seen[string(router.ModelAutomatic)])
}
