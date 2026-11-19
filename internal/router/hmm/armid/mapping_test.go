package armid_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/policy"
)

func TestGPT61SolRosterIdentity(t *testing.T) {
	model, ok := catalog.ByID("gpt-6.1-sol")
	require.True(t, ok)
	assert.Equal(t, "openai/gpt-6.1-sol", armid.ForModel(model))
	assert.Equal(t, "gpt-6.1-sol", armid.CatalogIDForRoster("openai/gpt-6.1-sol"))
	assert.Empty(t, policy.ValidateRosterIDs([]string{"openai/gpt-6.1-sol"}))
}

func TestGPT62RosterIdentity(t *testing.T) {
	for _, id := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		model, ok := catalog.ByID(id)
		require.True(t, ok, id)
		assert.Equal(t, "openai/"+id, armid.ForModel(model))
		assert.Equal(t, id, armid.CatalogIDForRoster("openai/"+id))
		assert.Empty(t, policy.ValidateRosterIDs([]string{"openai/" + id}))
	}
}

func TestParseAcceptsOnlyCanonicalEffortSuffixes(t *testing.T) {
	cases := map[string]armid.ArmID{
		"openai/gpt-5.5":           {Base: "openai/gpt-5.5"},
		"openai/gpt-5.5:high":      {Base: "openai/gpt-5.5", Effort: "high"},
		"openai/gpt-5.5:low":       {Base: "openai/gpt-5.5", Effort: "low"},
		"openai/gpt-5.5:med":       {Base: "openai/gpt-5.5:med"},
		"openai/gpt-5.5:xhigh":     {Base: "openai/gpt-5.5", Effort: "xhigh"},
		"openai/gpt-5.5:ultra":     {Base: "openai/gpt-5.5:ultra"},
		"openai/gpt-5.5:minimal":   {Base: "openai/gpt-5.5:minimal"},
		"openai/gpt-5.5:HIGH":      {Base: "openai/gpt-5.5:HIGH"},
		"openai/gpt-5.5:free":      {Base: "openai/gpt-5.5:free"},
		"openai/gpt-5.5:":          {Base: "openai/gpt-5.5:"},
		":high":                    {Base: ":high"},
		"vendor/model:free:high":   {Base: "vendor/model:free", Effort: "high"},
		"vendor/model:high:custom": {Base: "vendor/model:high:custom"},
	}
	for rosterArm, want := range cases {
		assert.Equal(t, want, armid.Parse(rosterArm), rosterArm)
	}
}
