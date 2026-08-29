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

func newOverrideAdapter(result policy.Result) *policy.SidecarRouter {
	resolver := policy.NewResolver(
		set("claude-opus-4-8", "claude-sonnet-5"),
		set(providers.ProviderAnthropic),
		func(model catalog.Model) string { return "anthropic/" + model.ID },
		policy.ManagedProviderPolicy(),
	)
	return policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy:    router.StrategyHMM,
		Unavailable: errors.New("override unavailable"),
	}, &recordingPolicy{result: result}, resolver)
}

func overrideTestResult() policy.Result {
	return policy.Result{
		SchemaVersion: policy.SchemaVersionV1,
		RouteID:       "route-override",
		Model:         "anthropic/claude-opus-4-8",
		Provider:      providers.ProviderAnthropic,
		Score:         0.8,
		PolicyGroup:   "maximum",
		DisplayMarker: "✦ **Weave Router** → Delegating work with claude-opus-4-8",
		RankedFallback: []policy.PreviewGroup{{
			Group:        "maximum",
			Probability:  0.8,
			RosterArms:   []string{"anthropic/claude-opus-4-8", "anthropic/claude-sonnet-5"},
			EligibleArms: []string{"anthropic/claude-opus-4-8", "anthropic/claude-sonnet-5"},
		}},
	}
}

func TestSelectionOverrideReplacesSidecarPick(t *testing.T) {
	adapter := newOverrideAdapter(overrideTestResult())
	adapter.WithSelectionOverride(func(_ context.Context, observation policy.SelectionObservation) (policy.SelectionPick, bool) {
		assert.Equal(t, "anthropic/claude-opus-4-8", observation.SidecarPick)
		assert.Equal(t, "maximum", observation.SidecarGroup)
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-sonnet-5"}, true
	})

	decision, err := adapter.Route(context.Background(), router.Request{})

	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Equal(t, providers.ProviderAnthropic, decision.Provider)
	assert.Contains(t, decision.Reason, ":go_selection")
	require.NotNil(t, decision.Metadata)
	assert.Equal(t, "anthropic/claude-sonnet-5", decision.Metadata.SelectedRosterArmID)
	assert.Equal(t, "maximum", decision.Metadata.PolicyGroup)
	assert.Empty(t, decision.Metadata.DisplayMarker,
		"a reselected arm must drop the sidecar's stale display marker")
}

func TestSelectionOverrideIdenticalDecisionWhenPickMatchesSidecar(t *testing.T) {
	baseline, err := newOverrideAdapter(overrideTestResult()).Route(context.Background(), router.Request{})
	require.NoError(t, err)

	adapter := newOverrideAdapter(overrideTestResult())
	adapter.WithSelectionOverride(func(_ context.Context, _ policy.SelectionObservation) (policy.SelectionPick, bool) {
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-opus-4-8"}, true
	})
	decision, err := adapter.Route(context.Background(), router.Request{})

	require.NoError(t, err)
	require.NotNil(t, decision.Metadata)
	require.NotNil(t, decision.Metadata.PinStickyOverrideEligible)
	assert.False(t, *decision.Metadata.PinStickyOverrideEligible,
		"a matching Go pick is still deterministic and must not be pin-sticky eligible")
	decision.Metadata.PinStickyOverrideEligible = nil
	assert.Equal(t, baseline, decision,
		"apart from pin-sticky neutralization, an override agreeing with the sidecar must leave the decision untouched")
}

func TestSelectionOverrideNeutralizesPinStickyWhenPickMatchesSidecar(t *testing.T) {
	eligible := true
	result := overrideTestResult()
	result.PinStickyOverrideEligible = &eligible
	result.Reason = "arm selector unavailable [pin_sticky_override_eligible]"

	adapter := newOverrideAdapter(result)
	adapter.WithSelectionOverride(func(_ context.Context, _ policy.SelectionObservation) (policy.SelectionPick, bool) {
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-opus-4-8"}, true
	})
	decision, err := adapter.Route(context.Background(), router.Request{})

	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-8", decision.Model)
	require.NotNil(t, decision.Metadata)
	require.NotNil(t, decision.Metadata.PinStickyOverrideEligible)
	assert.False(t, *decision.Metadata.PinStickyOverrideEligible)
	assert.NotContains(t, decision.Reason, "[pin_sticky_override_eligible]")
}

func TestSelectionOverrideFailsOpenWhenNoPick(t *testing.T) {
	baseline, err := newOverrideAdapter(overrideTestResult()).Route(context.Background(), router.Request{})
	require.NoError(t, err)

	adapter := newOverrideAdapter(overrideTestResult())
	adapter.WithSelectionOverride(func(_ context.Context, _ policy.SelectionObservation) (policy.SelectionPick, bool) {
		return policy.SelectionPick{}, false
	})
	decision, err := adapter.Route(context.Background(), router.Request{})

	require.NoError(t, err)
	assert.Equal(t, baseline, decision)
}

func TestSelectionOverrideYieldsToClusterOverride(t *testing.T) {
	adapter := newOverrideAdapter(overrideTestResult())
	overrideCalled := false
	adapter.WithSelectionOverride(func(_ context.Context, _ policy.SelectionObservation) (policy.SelectionPick, bool) {
		overrideCalled = true
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-opus-4-8"}, true
	})

	decision, err := adapter.Route(context.Background(), router.Request{
		ClusterArmOverrides: map[string][]string{
			"maximum": {"claude-sonnet-5", "claude-opus-4-8"},
		},
	})

	require.NoError(t, err)
	assert.False(t, overrideCalled,
		"an applied per-key cluster override must take precedence over the selection override")
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Contains(t, decision.Reason, ":cluster_override")
}

func TestSelectionOverrideFiresWhenOverridesOmitWinningGroup(t *testing.T) {
	adapter := newOverrideAdapter(overrideTestResult())
	adapter.WithSelectionOverride(func(_ context.Context, _ policy.SelectionObservation) (policy.SelectionPick, bool) {
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-sonnet-5"}, true
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

func TestSelectionOverrideNeutralizesPinStickySignal(t *testing.T) {
	eligible := true
	result := overrideTestResult()
	result.PinStickyOverrideEligible = &eligible
	result.Reason = "arm selector unavailable [pin_sticky_override_eligible]"

	adapter := newOverrideAdapter(result)
	adapter.WithSelectionOverride(func(_ context.Context, _ policy.SelectionObservation) (policy.SelectionPick, bool) {
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-sonnet-5"}, true
	})
	decision, err := adapter.Route(context.Background(), router.Request{})

	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	require.NotNil(t, decision.Metadata)
	require.NotNil(t, decision.Metadata.PinStickyOverrideEligible)
	assert.False(t, *decision.Metadata.PinStickyOverrideEligible,
		"a Go-selected arm is deterministic and must not be pin-sticky eligible")
	assert.NotContains(t, decision.Reason, "[pin_sticky_override_eligible]",
		"the legacy sentinel must not survive a Go reselection")
}

func TestSelectionOverrideNotFiringLeavesPinStickySignalIntact(t *testing.T) {
	eligible := true
	result := overrideTestResult()
	result.PinStickyOverrideEligible = &eligible
	result.Reason = "arm selector unavailable [pin_sticky_override_eligible]"

	adapter := newOverrideAdapter(result)
	adapter.WithSelectionOverride(func(_ context.Context, _ policy.SelectionObservation) (policy.SelectionPick, bool) {
		return policy.SelectionPick{}, false
	})
	decision, err := adapter.Route(context.Background(), router.Request{})

	require.NoError(t, err)
	require.NotNil(t, decision.Metadata)
	require.NotNil(t, decision.Metadata.PinStickyOverrideEligible)
	assert.True(t, *decision.Metadata.PinStickyOverrideEligible)
	assert.Contains(t, decision.Reason, "[pin_sticky_override_eligible]")
}

func TestSidecarRouterPropagatesTypedPinStickyField(t *testing.T) {
	eligible := true
	result := overrideTestResult()
	result.PinStickyOverrideEligible = &eligible
	decision, err := newOverrideAdapter(result).Route(context.Background(), router.Request{})

	require.NoError(t, err)
	require.NotNil(t, decision.Metadata)
	require.NotNil(t, decision.Metadata.PinStickyOverrideEligible)
	assert.True(t, *decision.Metadata.PinStickyOverrideEligible)

	decision, err = newOverrideAdapter(overrideTestResult()).Route(context.Background(), router.Request{})
	require.NoError(t, err)
	require.NotNil(t, decision.Metadata)
	assert.Nil(t, decision.Metadata.PinStickyOverrideEligible,
		"a sidecar that does not report the typed field must leave it nil")
}
