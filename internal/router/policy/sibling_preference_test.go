package policy_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/policy"
)

func TestSessionSiblingPreferenceSurvivesClusterChanges(t *testing.T) {
	const primary = "claude-opus-4-8"
	const sibling = "claude-sonnet-5"
	adapter := newSelectorAdapter(classifierOnlyResult())
	group := escalation.High
	adapter.WithArmSelector(func(context.Context, policy.SelectionInput) (policy.SelectionPick, error) {
		fallback := classifierFallback(string(group))
		return policy.SelectionPick{
			Group: string(group), Arm: fallback[0].EligibleArms[0], RankedFallback: fallback,
		}, nil
	})
	req := router.Request{SiblingPreferences: map[escalation.Group]router.SiblingPreference{
		escalation.High: {Model: sibling, DisplacedModels: []string{primary}},
	}}
	for _, next := range []escalation.Group{escalation.High, escalation.Medium, escalation.High} {
		group = next
		decision, err := adapter.Route(context.Background(), req)
		require.NoError(t, err)
		if next == escalation.High {
			assert.Equal(t, sibling, decision.Model)
			assert.Equal(t, []string{sibling, primary}, decision.Metadata.RescueModels)
		} else {
			assert.Equal(t, primary, decision.Model, "a different group keeps its own ranking")
		}
	}

	req.ExcludedModels = set(sibling)
	decision, err := adapter.Route(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, primary, decision.Model, "a two-model roster retains its last resort")

	req.ExcludedModels = set(primary, sibling)
	_, err = adapter.Route(context.Background(), req)
	assert.Error(t, err, "an exhausted roster cannot widen to unrelated models")

	req.ExcludedModels = nil
	req.ClusterArmOverrides = map[string][]string{string(escalation.High): {primary}}
	decision, err = adapter.Route(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, primary, decision.Model, "the cluster allowlist still constrains the preference")

	req.ClusterArmOverrides = nil
	req.SiblingPreferences[escalation.High] = router.SiblingPreference{Model: catalog.ModelIDGPT56Luna.String()}
	decision, err = adapter.Route(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, primary, decision.Model, "a remembered model absent from the roster cannot enter it")
}

func TestSessionSiblingPreferencePreservesEffortQualifiedArms(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	adapter.WithArmSelector(func(context.Context, policy.SelectionInput) (policy.SelectionPick, error) {
		return policy.SelectionPick{Group: string(escalation.High), Arm: "anthropic/claude-opus-4-8:high", RankedFallback: []policy.PreviewGroup{{
			Group: string(escalation.High), EligibleArms: []string{"anthropic/claude-opus-4-8:high", "anthropic/claude-sonnet-5:high"},
		}}}, nil
	})
	req := router.Request{SiblingPreferences: map[escalation.Group]router.SiblingPreference{
		escalation.High: {Model: "claude-sonnet-5", DisplacedModels: []string{catalog.ModelIDClaudeOpus48.String()}},
	}}
	decision, err := adapter.Route(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Equal(t, "high", decision.Effort)
	assert.Equal(t, []string{"claude-sonnet-5", catalog.ModelIDClaudeOpus48.String()}, decision.Metadata.RescueModels)
}

func TestSiblingGroupIncludesSoftExcludedLastResort(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	adapter.WithArmSelector(func(context.Context, policy.SelectionInput) (policy.SelectionPick, error) {
		return policy.SelectionPick{Group: string(escalation.High), Arm: "anthropic/claude-sonnet-5", RankedFallback: classifierFallback(string(escalation.High))}, nil
	})
	primary := catalog.ModelIDClaudeOpus48.String()
	req := router.Request{
		AutomaticExcludedModels: set(primary),
		SiblingPreferences: map[escalation.Group]router.SiblingPreference{
			escalation.High: {Model: "claude-sonnet-5", DisplacedModels: []string{primary}},
		},
	}
	decision, err := adapter.Route(context.Background(), req)
	require.NoError(t, err)
	assert.Contains(t, decision.Metadata.SiblingModels, primary, "a readmitted last-resort winner can become the new preference")
	req.ExcludedModels = set(primary)
	decision, err = adapter.Route(context.Background(), req)
	require.NoError(t, err)
	assert.NotContains(t, decision.Metadata.SiblingModels, primary, "hard exclusions cannot be remembered as eligible")
}

func TestSiblingPreferenceAppliesWithoutUserText(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	adapter.WithArmSelector(func(_ context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		require.True(t, input.Unscorable)
		return policy.SelectionPick{Group: string(escalation.High), Arm: "anthropic/claude-opus-4-8", RankedFallback: classifierFallback(string(escalation.High))}, nil
	})
	req := router.Request{SiblingPreferences: map[escalation.Group]router.SiblingPreference{
		escalation.High: {Model: "claude-sonnet-5", DisplacedModels: []string{catalog.ModelIDClaudeOpus48.String()}},
	}}
	decision, err := adapter.RouteWithoutUserText(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Equal(t, []string{"claude-sonnet-5", catalog.ModelIDClaudeOpus48.String()}, decision.Metadata.RescueModels)
}
