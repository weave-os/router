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

func TestEscalatingRescueGroups_ExhaustsSelectedClassBeforeHigherClasses(t *testing.T) {
	ranked := []policy.PreviewGroup{
		{Group: "high", EligibleArms: []string{"anthropic/opus"}},
		{Group: "low", EligibleArms: []string{"openai/astra", "anthropic/haiku"}},
		{Group: "maximum", EligibleArms: []string{"openai/nova"}},
		{Group: "medium", EligibleArms: []string{"openai/luna"}},
	}
	resolved := resolvedFor(map[string]string{
		"astra": "openai/astra", "haiku": "anthropic/haiku",
		"luna": "openai/luna", "opus": "anthropic/opus", "nova": "openai/nova",
	})

	got := policy.RescueModelOrder(nil, policy.EscalatingRescueGroups("low", "", ranked), resolved)
	assert.Equal(t, []string{"astra", "haiku", "luna", "opus", "nova"}, got)
	assert.Equal(t, []string{"luna", "opus", "nova"},
		policy.RescueModelOrder(nil, policy.EscalatingRescueGroups("medium", "", ranked), resolved))
	assert.Equal(t, []string{"astra", "haiku"},
		policy.RescueModelOrder(nil, policy.EscalatingRescueGroups("low", "low", ranked), resolved))
	assert.Nil(t, policy.EscalatingRescueGroups("low", "absent", ranked))
}

func TestEscalatingRescueGroups_HonorsPerKeyRosterRestrictions(t *testing.T) {
	ranked := []policy.PreviewGroup{
		{Group: "low", EligibleArms: []string{"openai/astra", "anthropic/haiku"}},
		{Group: "medium", EligibleArms: []string{"openai/luna"}},
	}
	resolved := resolvedFor(map[string]string{
		"astra": "openai/astra", "haiku": "anthropic/haiku", "luna": "openai/luna",
	})
	overrides := map[string][]string{"low": {"haiku"}}

	got := policy.RescueModelOrder(overrides, policy.EscalatingRescueGroups("low", "", ranked), resolved)
	assert.Equal(t, []string{"haiku", "luna"}, got)
}
