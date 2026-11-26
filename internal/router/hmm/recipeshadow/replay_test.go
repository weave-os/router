package recipeshadow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/recipeshadow"
	"weave-os/router/internal/router/rosterdata"
)

const (
	efficientProfileKey = "00000000-0000-5000-8000-0000000000e1"
	efficientPolicySHA  = "sha256:efficient"
	opusArm             = "anthropic/claude-opus-5.5"
	sonnetArm           = "anthropic/claude-sonnet-5.5"
	haikuArm            = "anthropic/claude-haiku-4.5"
)

type fakeRosters struct {
	roster *rosterdata.Roster
	err    error
	asked  []string
}

func (f *fakeRosters) ProfileRoster(_ context.Context, profileKey string) (recipeshadow.ProfileRoster, error) {
	f.asked = append(f.asked, profileKey)
	if f.err != nil {
		return recipeshadow.ProfileRoster{}, f.err
	}
	return recipeshadow.ProfileRoster{Roster: f.roster, PolicySHA256: efficientPolicySHA}, nil
}

func efficientRoster() *rosterdata.Roster {
	return &rosterdata.Roster{
		ClassOrder: []string{"low", "high"},
		Clusters: map[string]rosterdata.Cluster{
			"low":  {Arms: []string{haikuArm, sonnetArm}},
			"high": {Arms: []string{sonnetArm, opusArm}},
		},
		SHA256: "roster-efficient",
	}
}

func servedHMMDecision() router.Decision {
	return router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-opus-5-5",
		Reason:   "hmm_policy(label=high)",
		Metadata: &router.RoutingMetadata{
			Strategy:                 string(router.StrategyHMM),
			RouteID:                  "route-1",
			PolicyGroup:              "high",
			SelectedRosterArmID:      opusArm,
			ClassifierPredictedLabel: "high",
			ClassifierClassOrder:     []string{"low", "high"},
			ClassifierProbabilities:  map[string]float64{"low": 0.2, "high": 0.8},
		},
	}
}

func anthropicRequest() router.Request {
	return router.Request{
		ClientApp:        "claude-code",
		EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
	}
}

func TestReplayPicksFromShadowRosterUsingServedClassifierFacts(t *testing.T) {
	rosters := &fakeRosters{roster: efficientRoster()}
	replayer := recipeshadow.NewReplayer(rosters, map[string]struct{}{providers.ProviderAnthropic: {}})

	pick, err := replayer.Replay(context.Background(), efficientProfileKey, servedHMMDecision(), anthropicRequest())

	require.NoError(t, err)
	assert.Equal(t, []string{efficientProfileKey}, rosters.asked)
	assert.Equal(t, "claude-sonnet-5-5", pick.Model, "the efficient roster leads the high group with Sonnet")
	assert.Equal(t, providers.ProviderAnthropic, pick.Provider)
	assert.Equal(t, sonnetArm, pick.RosterArmID)
	assert.Equal(t, "high", pick.PolicyGroup)
	assert.Equal(t, efficientPolicySHA, pick.PolicySHA256)
}

func TestReplayRespectsTheServedRequestsEligibility(t *testing.T) {
	replayer := recipeshadow.NewReplayer(&fakeRosters{roster: efficientRoster()}, map[string]struct{}{providers.ProviderAnthropic: {}})
	req := anthropicRequest()
	req.Candidates.ExcludedModels = map[string]struct{}{"claude-sonnet-5-5": {}}

	pick, err := replayer.Replay(context.Background(), efficientProfileKey, servedHMMDecision(), req)

	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5-5", pick.Model, "an excluded model is never a shadow pick")
}

func TestServedFactsRequireAnHMMClassifierDecision(t *testing.T) {
	replayer := recipeshadow.NewReplayer(&fakeRosters{roster: efficientRoster()}, nil)

	facts, scored := replayer.ServedFacts(servedHMMDecision())
	require.True(t, scored)
	assert.Equal(t, router.RecipeShadowServed{RosterArmID: opusArm, PolicyGroup: "high", PredictedLabel: "high"}, facts)

	for name, decision := range map[string]router.Decision{
		"pinned passthrough":  {Model: "claude-opus-5-5", Reason: "cluster_argmax"},
		"no classifier facts": {Model: "claude-opus-5-5", Reason: "hmm_policy", Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMM)}},
	} {
		_, scored := replayer.ServedFacts(decision)
		assert.False(t, scored, name)
		_, err := replayer.Replay(context.Background(), efficientProfileKey, decision, anthropicRequest())
		assert.ErrorIs(t, err, router.ErrRecipeShadowNoClassifierFacts, name)
	}
}

func TestReplayReportsRosterLoadFailure(t *testing.T) {
	loadErr := errors.New("selection set unavailable")
	replayer := recipeshadow.NewReplayer(&fakeRosters{err: loadErr}, map[string]struct{}{providers.ProviderAnthropic: {}})

	_, err := replayer.Replay(context.Background(), efficientProfileKey, servedHMMDecision(), anthropicRequest())

	require.ErrorIs(t, err, loadErr)
}
