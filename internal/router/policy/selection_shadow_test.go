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

func TestSelectionShadowObservesWithoutChangingDecision(t *testing.T) {
	strategy := router.Strategy("shadow-policy")
	result := policy.Result{
		SchemaVersion:        policy.SchemaVersionV1,
		RouteID:              "route-shadow",
		Model:                "shadow/gpt-5.5",
		Provider:             providers.ProviderOpenAI,
		Score:                0.8,
		PolicyGroup:          "balanced",
		PolicyArtifactID:     "shadow-prod",
		PolicyArtifactSHA256: "sha256:shadow",
		RosterVersion:        "roster-1",
		RankedFallback: []policy.PreviewGroup{
			{Group: "balanced", Probability: 0.8, RosterArms: []string{"shadow/gpt-5.5"}, EligibleArms: []string{"shadow/gpt-5.5"}},
			{Group: "high", Probability: 0.2, RosterArms: []string{"shadow/gpt-6"}, EligibleArms: nil},
		},
	}
	newAdapter := func() *policy.SidecarRouter {
		decider := &recordingPolicy{result: result}
		resolver := policy.NewResolver(
			set("gpt-5.5"),
			set(providers.ProviderOpenAI),
			func(model catalog.Model) string { return "shadow/" + model.ID },
			policy.ManagedProviderPolicy(),
		)
		return policy.NewSidecarRouter(policy.SidecarRouterConfig{
			Strategy:    strategy,
			Unavailable: errors.New("shadow unavailable"),
		}, decider, resolver)
	}
	req := router.Request{
		OrganizationID: "org-1",
		ClientApp:      "claude-code",
	}

	baseline, err := newAdapter().Route(context.Background(), req)
	require.NoError(t, err)

	shadowed := newAdapter()
	var observed []policy.SelectionObservation
	shadowed.WithSelectionShadow(func(_ context.Context, observation policy.SelectionObservation) {
		observed = append(observed, observation)
	})
	decision, err := shadowed.Route(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, baseline, decision)
	require.Len(t, observed, 1)
	observation := observed[0]
	assert.Equal(t, strategy, observation.Strategy)
	assert.Equal(t, policy.ExecutionModeServing, observation.ExecutionMode)
	assert.Equal(t, "route-shadow", observation.RouteID)
	assert.Equal(t, "claude-code", observation.Harness)
	assert.Equal(t, "balanced", observation.SidecarGroup)
	assert.Equal(t, "shadow/gpt-5.5", observation.SidecarPick)
	assert.Equal(t, result.RankedFallback, observation.RankedFallback)
	assert.Equal(t, []string{"shadow/gpt-5.5"}, observation.CandidateRosterIDs)
}
