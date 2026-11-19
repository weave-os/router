package policy_test

import (
	"testing"

	"weave-os/router/internal/router/policy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRosterIDs_UnknownVendorReported(t *testing.T) {
	diags := policy.ValidateRosterIDs([]string{"newvendor/model-x"})

	require.Len(t, diags, 1)
	assert.Equal(t, "newvendor/model-x", diags[0].RosterID)
	assert.Equal(t, policy.ExclusionUnknownCatalogModel, diags[0].Reason)
}

func TestValidateRosterIDs_AliasMappedArmIsValid(t *testing.T) {
	// "x-ai/grok-4.5" only maps back to the bare catalog ID "grok-4.5"
	// through rosterAliases; it must not be reported as unknown.
	assert.Empty(t, policy.ValidateRosterIDs([]string{"x-ai/grok-4.5"}))
}

func TestValidateRosterIDs_EffortSuffixedArmIsValid(t *testing.T) {
	assert.Empty(t, policy.ValidateRosterIDs([]string{"openai/gpt-5.6-sol:high"}))
}

func TestValidateRosterIDs_MixedRosterReportsOnlyBadArms(t *testing.T) {
	diags := policy.ValidateRosterIDs([]string{
		"openai/gpt-5.6-sol",
		"newvendor/model-x",
		"anthropic/claude-opus-4.8",
	})

	require.Len(t, diags, 1)
	assert.Equal(t, "newvendor/model-x", diags[0].RosterID)
}

func TestValidateRosterIDs_EmptyInput(t *testing.T) {
	assert.Empty(t, policy.ValidateRosterIDs(nil))
}
