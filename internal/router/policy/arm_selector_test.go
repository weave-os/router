package policy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/router/policy"
)

func newSelectorAdapter(result policy.Result) *policy.SidecarRouter {
	resolver := policy.NewResolver(
		set("claude-opus-4-8", "claude-sonnet-5"),
		set(providers.ProviderAnthropic),
		func(model catalog.Model) string { return "anthropic/" + model.ID },
		policy.ManagedProviderPolicy(),
	)
	return policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy:    router.StrategyHMM,
		Unavailable: errors.New("selection unavailable"),
	}, &recordingPolicy{result: result}, resolver)
}

func classifierOnlyResult() policy.Result {
	return policy.Result{
		SchemaVersion: policy.SchemaVersionV3,
		RouteID:       "route-classifier",
		Score:         0.8,
		PolicyGroup:   "maximum",
		RankedFallback: []policy.PreviewGroup{{
			Group:        "maximum",
			Probability:  0.8,
			RosterArms:   []string{"anthropic/claude-opus-4-8", "anthropic/claude-sonnet-5"},
			EligibleArms: []string{"anthropic/claude-opus-4-8", "anthropic/claude-sonnet-5"},
		}},
	}
}

func TestArmSelectorPickIsServed(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	adapter.WithArmSelector(func(_ context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		assert.Equal(t, "maximum", input.ClassifierGroup)
		assert.ElementsMatch(t, []string{"anthropic/claude-opus-4-8", "anthropic/claude-sonnet-5"}, input.CandidateRosterIDs)
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-sonnet-5"}, nil
	})

	decision, err := adapter.Route(context.Background(), router.Request{})

	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Equal(t, providers.ProviderAnthropic, decision.Provider)
	assert.Contains(t, decision.Reason, ":go_selection")
	require.NotNil(t, decision.Metadata)
	assert.Equal(t, "anthropic/claude-sonnet-5", decision.Metadata.SelectedRosterArmID)
	assert.Equal(t, "maximum", decision.Metadata.PolicyGroup)
}

func TestArmSelectorErrorFailsTheTurn(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	adapter.WithArmSelector(func(_ context.Context, _ policy.SelectionInput) (policy.SelectionPick, error) {
		return policy.SelectionPick{}, errors.New("no eligible arm")
	})

	_, err := adapter.Route(context.Background(), router.Request{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "selection unavailable",
		"a failed selection must surface as the strategy's unavailable sentinel, not a sidecar-picked arm")
}

func TestArmSelectorRejectsLegacySchema(t *testing.T) {
	result := classifierOnlyResult()
	result.SchemaVersion = policy.SchemaVersionV1
	result.Model = "anthropic/claude-opus-4-8"

	adapter := newSelectorAdapter(result)
	called := false
	adapter.WithArmSelector(func(_ context.Context, _ policy.SelectionInput) (policy.SelectionPick, error) {
		called = true
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-opus-4-8"}, nil
	})

	_, err := adapter.Route(context.Background(), router.Request{})

	require.Error(t, err)
	assert.False(t, called, "a legacy response must be rejected before selection runs")
	assert.Contains(t, err.Error(), policy.SchemaVersionV3)
}

func TestArmSelectorNegotiatesV3(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8"),
		set(providers.ProviderAnthropic),
		func(model catalog.Model) string { return "anthropic/" + model.ID },
		policy.ManagedProviderPolicy(),
	)
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy:    router.StrategyHMM,
		Unavailable: errors.New("selection unavailable"),
	}, &recordingPolicy{result: classifierOnlyResult()}, resolver)

	assert.Equal(t, policy.SchemaVersionV1, resolver.SchemaVersion())
	adapter.WithArmSelector(func(_ context.Context, _ policy.SelectionInput) (policy.SelectionPick, error) {
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-opus-4-8"}, nil
	})
	assert.Equal(t, policy.SchemaVersionV3, resolver.SchemaVersion())
}

func TestArmSelectorYieldsToClusterOverride(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	adapter.WithArmSelector(func(_ context.Context, _ policy.SelectionInput) (policy.SelectionPick, error) {
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-opus-4-8"}, nil
	})

	decision, err := adapter.Route(context.Background(), router.Request{
		ClusterArmOverrides: map[string][]string{
			"maximum": {"claude-sonnet-5", "claude-opus-4-8"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Contains(t, decision.Reason, ":cluster_override")
}

func TestArmSelectorSurvivesOverridesOmittingWinningGroup(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	adapter.WithArmSelector(func(_ context.Context, _ policy.SelectionInput) (policy.SelectionPick, error) {
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-sonnet-5"}, nil
	})

	// A partial per-key map that configures only an unrelated cluster must not
	// suppress Go selection for the served group.
	decision, err := adapter.Route(context.Background(), router.Request{
		ClusterArmOverrides: map[string][]string{
			"minimal": {"claude-sonnet-5"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Contains(t, decision.Reason, ":go_selection")
}
