package selectionpolicy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selectionpolicy/selectionpolicytest"
)

func TestDeployedModelsMapRosterArmsToCatalogEntriesOncePerModel(t *testing.T) {
	admitted := selectionpolicytest.Admit(t, rosterdata.Roster{Clusters: map[string]rosterdata.Cluster{
		"low": {Arms: []string{
			"openai/gpt-5.6-sol",
			"openai/gpt-5.6-sol:high",
			"anthropic/claude-opus-4.8",
			"x-ai/grok-4.7",
			"meta/muse-spark-1.3",
			"meta/muse-spark-1.3:xhigh",
		}},
	}})

	assert.ElementsMatch(t, []router.DeployedEntry{
		{Model: "gpt-5.6-sol", Provider: providers.ProviderOpenAI},
		{Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic},
		{Model: "grok-4.7", Provider: providers.ProviderXAI},
		{Model: "muse-spark-1.3", Provider: providers.ProviderMeta},
	}, admitted.DeployedModels())
}

func TestHarnessClusterArmsFallBackToClusterArmsPerCluster(t *testing.T) {
	admitted := selectionpolicytest.Admit(t, rosterdata.Roster{Clusters: map[string]rosterdata.Cluster{
		"low":  {Arms: []string{"openai/gpt-5.6-sol"}, ArmsByHarness: map[rosterdata.Harness][]string{"codex": {"openai/gpt-5.6-sol-pro"}}},
		"high": {Arms: []string{"anthropic/claude-opus-4.8"}},
	}})

	assert.Equal(t, map[string][]string{
		"low":  {"openai/gpt-5.6-sol"},
		"high": {"anthropic/claude-opus-4.8"},
	}, admitted.ClusterArms())
	assert.Equal(t, map[string]map[string][]string{
		"codex": {"low": {"openai/gpt-5.6-sol-pro"}, "high": {"anthropic/claude-opus-4.8"}},
	}, admitted.HarnessClusterArms())
}
