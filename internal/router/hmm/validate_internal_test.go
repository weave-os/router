package hmm

import (
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/router/policy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRosterIDs_AmbiguousMappingReported(t *testing.T) {
	// Two catalog models with the same slash-form ID map to one roster ID.
	models := []catalog.Model{
		{ID: "acme/model-a", Providers: []catalog.ProviderBinding{{Provider: providers.ProviderOpenAI}}},
		{ID: "acme/model-a", Providers: []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}},
	}

	diags := validateRosterIDs([]string{"acme/model-a"}, models, policy.ManagedProviderPolicy())

	require.Len(t, diags, 1)
	assert.Equal(t, policy.ExclusionAmbiguousRoster, diags[0].Reason)
	assert.Equal(t, "acme/model-a", diags[0].RosterID)
}

func TestValidateRosterIDs_OpenRouterOnlyBindingReported(t *testing.T) {
	models := []catalog.Model{
		{ID: "acme/model-b", Providers: []catalog.ProviderBinding{{Provider: providers.ProviderOpenRouter}}},
	}

	diags := validateRosterIDs([]string{"acme/model-b"}, models, policy.ManagedProviderPolicy())

	require.Len(t, diags, 1)
	assert.Equal(t, policy.ExclusionProviderPolicy, diags[0].Reason)
	assert.Equal(t, "acme/model-b", diags[0].CatalogID)
}
