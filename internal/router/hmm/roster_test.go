package hmm_test

import (
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/hmm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeployedEntriesForRoster_MapsRosterSlugsToCatalogEntries(t *testing.T) {
	// Roster slugs are provider-prefixed (openai/…); entries carry bare
	// catalog IDs and primary provider, matching the cluster source shape.
	got, err := hmm.DeployedEntriesForRoster([]string{
		"openai/gpt-5.6-sol",
		"openai/gpt-5.6-sol-pro",
		"openai/gpt-5.6-luna-pro",
		"anthropic/claude-opus-4.8",
		"deepseek/deepseek-v4-flash",
		"x-ai/grok-4.6",
		"x-ai/grok-4.7",
	})
	require.NoError(t, err)

	byModel := make(map[string]string, len(got))
	for _, e := range got {
		byModel[e.Model] = e.Provider
	}

	assert.Equal(t, providers.ProviderOpenAI, byModel["gpt-5.6-sol"])
	assert.Equal(t, providers.ProviderOpenAI, byModel["gpt-5.6-sol-pro"])
	assert.Equal(t, providers.ProviderOpenAI, byModel["gpt-5.6-luna-pro"])
	assert.Equal(t, providers.ProviderAnthropic, byModel["claude-opus-4-8"])
	// Bare first-party xAI IDs map through an explicit roster alias; the
	// provider is the native xAI binding.
	assert.Equal(t, providers.ProviderXAI, byModel["grok-4.6"])
	assert.Equal(t, providers.ProviderXAI, byModel["grok-4.7"])
	// OSS slugs already carry their provider prefix, so the roster_id equals
	// the catalog ID; provider is whatever the catalog lists first.
	require.Contains(t, byModel, "deepseek/deepseek-v4-flash")
	assert.NotEmpty(t, byModel["deepseek/deepseek-v4-flash"])
}

func TestDeployedEntriesForRoster_PreservesOrderAndCollapsesDuplicates(t *testing.T) {
	got, err := hmm.DeployedEntriesForRoster([]string{
		"openai/gpt-5.6-sol-pro",
		"openai/gpt-5.6-sol",
		"openai/gpt-5.6-sol:high",
	})

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "gpt-5.6-sol-pro", got[0].Model)
	assert.Equal(t, "gpt-5.6-sol", got[1].Model)
}

func TestDeployedEntriesForRoster_RejectsArmsWithoutCatalogModel(t *testing.T) {
	_, err := hmm.DeployedEntriesForRoster([]string{"openai/gpt-5.6-sol", "not/a-real-roster-id"})
	require.ErrorContains(t, err, "not/a-real-roster-id")
}

func TestDeployedEntriesForRoster_EmptyInput(t *testing.T) {
	got, err := hmm.DeployedEntriesForRoster(nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}
