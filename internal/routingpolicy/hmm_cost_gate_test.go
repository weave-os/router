package routingpolicy_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

var (
	stayModel  = string(catalog.ModelIDClaudeHaiku45)
	otherModel = string(catalog.ModelIDClaudeSonnet46)
)

func hmmGate() routingpolicy.HMMCostGate {
	return routingpolicy.HMMCostGate{
		RegisteredProviders: func() map[string]struct{} {
			return map[string]struct{}{providers.ProviderAnthropic: {}}
		},
	}
}

func hmmPin(reason string, endedAgo time.Duration) sessionpin.Pin {
	return sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           stayModel,
		Reason:          reason,
		LastTurnEndedAt: time.Now().Add(-endedAgo),
		PinnedUntil:     time.Now().Add(time.Hour),
	}
}

func TestHMMCostGateDecideWithoutStayPinSwitches(t *testing.T) {
	fresh := router.Decision{Provider: providers.ProviderAnthropic, Model: otherModel}
	stay, plan, sticky := hmmGate().Decide(router.Request{}, sessionpin.Pin{}, sessionpin.Pin{}, fresh, 1000, false)
	assert.False(t, sticky)
	assert.Empty(t, stay.Model)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, planner.ReasonNoPin, plan.Reason)
}

func TestHMMCostGateStayPinIgnoresNonHMMActivePin(t *testing.T) {
	_, ok := hmmGate().StayPin(router.Request{}, hmmPin("cluster_scorer", time.Minute), sessionpin.Pin{})
	assert.False(t, ok, "active pin not written by HMM")

	got, ok := hmmGate().StayPin(router.Request{}, hmmPin("hmm_policy", time.Minute), sessionpin.Pin{})
	require.True(t, ok)
	assert.Equal(t, stayModel, got.Model)
}

func TestHMMCostGateStayPinPrefersMostRecentTurn(t *testing.T) {
	older := hmmPin("hmm_policy", time.Hour)
	newer := hmmPin(routingpolicy.HMMHistoryReason, time.Minute)
	newer.Model = otherModel
	got, ok := hmmGate().StayPin(router.Request{}, older, newer)
	require.True(t, ok)
	assert.Equal(t, otherModel, got.Model)
}

func TestHMMCostGateNormalizeStayPinRejectsIneligibleModels(t *testing.T) {
	pin := hmmPin("hmm_policy", time.Minute)
	unavailable := hmmGate()
	unavailable.AvailableModels = map[string]struct{}{otherModel: {}}
	cases := map[string]struct {
		gate routingpolicy.HMMCostGate
		req  router.Request
	}{
		"excluded":             {hmmGate(), router.Request{ExcludedModels: map[string]struct{}{stayModel: {}}}},
		"auto-disabled":        {hmmGate(), router.Request{AutomaticExcludedModels: map[string]struct{}{stayModel: {}}}},
		"unavailable":          {unavailable, router.Request{}},
		"provider not enabled": {hmmGate(), router.Request{EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, ok := tc.gate.NormalizeStayPin(tc.req, pin)
			assert.False(t, ok)
		})
	}
	_, ok := hmmGate().NormalizeStayPin(router.Request{}, pin)
	assert.True(t, ok, "eligible pin")
}

func TestHMMCostGateNormalizeStayPinRejectsStalePins(t *testing.T) {
	expired := hmmPin("hmm_policy", time.Minute)
	expired.PinnedUntil = time.Now().Add(-time.Second)
	noTurn := hmmPin("hmm_policy", 0)
	noTurn.LastTurnEndedAt = time.Time{}
	maxedOut := hmmPin("hmm_policy", time.Minute)
	maxedOut.LastOutputLimitAt = maxedOut.LastTurnEndedAt
	for name, pin := range map[string]sessionpin.Pin{"expired": expired, "no prior turn": noTurn, "maxed out": maxedOut} {
		t.Run(name, func(t *testing.T) {
			_, ok := hmmGate().NormalizeStayPin(router.Request{}, pin)
			assert.False(t, ok)
		})
	}
}

func TestBaseModelOfStripsEffort(t *testing.T) {
	gpt := string(catalog.ModelIDGPT55)
	assert.Equal(t, gpt, routingpolicy.BaseModelOf(gpt+":high"))
	assert.Equal(t, stayModel, routingpolicy.BaseModelOf(stayModel))
}
