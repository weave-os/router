package policy_test

import (
	"testing"

	"weave-os/router/internal/router/policy"

	"github.com/stretchr/testify/assert"
)

func TestRescueModelOrder_FollowsRankedFallbackNotCatalogOrder(t *testing.T) {
	ranked := []policy.PreviewGroup{
		{Group: "maximum", EligibleArms: []string{"openai/astra"}},
		{Group: "high", EligibleArms: []string{"anthropic/opus"}},
		{Group: "medium", EligibleArms: []string{"openai/luna"}},
		{Group: "low", EligibleArms: []string{"openai/luna", "anthropic/haiku"}},
	}
	resolved := resolvedFor(map[string]string{
		"astra": "openai/astra", "opus": "anthropic/opus", "luna": "openai/luna", "haiku": "anthropic/haiku",
	})

	got := policy.RescueModelOrder(nil, ranked, resolved)
	assert.Equal(t, []string{"astra", "opus", "luna", "haiku"}, got,
		"group rank decides the order and a model listed in two groups appears once")
}

func TestRescueModelOrder_PerKeyExclusionNeverReenters(t *testing.T) {
	ranked := []policy.PreviewGroup{
		{Group: "maximum", EligibleArms: []string{"openai/astra"}},
		{Group: "high", EligibleArms: []string{"anthropic/opus"}},
		{Group: "medium", EligibleArms: []string{"openai/luna"}},
	}
	resolved := resolvedFor(map[string]string{
		"astra": "openai/astra", "opus": "anthropic/opus", "luna": "openai/luna",
	})
	// The key emptied the maximum slot (astra unchecked); the next groups' own
	// arms carry the rescue.
	overrides := map[string][]string{"maximum": {"not-deployed-model"}}

	got := policy.RescueModelOrder(overrides, ranked, resolved)
	assert.Equal(t, []string{"opus", "luna"}, got)
}

func TestRescueModelOrder_SkipsArmsWithoutABinding(t *testing.T) {
	ranked := []policy.PreviewGroup{
		{Group: "maximum", EligibleArms: []string{"openai/astra", "vendor/unresolved"}},
	}
	resolved := resolvedFor(map[string]string{"astra": "openai/astra"})

	assert.Equal(t, []string{"astra"}, policy.RescueModelOrder(nil, ranked, resolved))
	assert.Nil(t, policy.RescueModelOrder(nil, nil, resolved), "no ranked fallback yields no order")
}
