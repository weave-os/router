package hmm_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

var (
	stayModel  = string(catalog.ModelIDClaudeHaiku45)
	otherModel = string(catalog.ModelIDClaudeSonnet46)
)

func hmmGate() hmm.CostGate {
	return hmm.CostGate{
		RegisteredProviders: func() map[string]struct{} {
			return map[string]struct{}{providers.ProviderAnthropic: {}}
		},
	}
}

var gateNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func hmmPin(reason string, endedAgo time.Duration) sessionpin.Pin {
	return sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           stayModel,
		Reason:          reason,
		LastTurnEndedAt: gateNow.Add(-endedAgo),
		PinnedUntil:     gateNow.Add(time.Hour),
	}
}

func TestHMMCostGateDecideWithoutStayPinSwitches(t *testing.T) {
	stay, plan, sticky := hmmGate().Decide(hmm.CostGateInput{
		Fresh: router.Decision{Provider: providers.ProviderAnthropic, Model: otherModel}, EstimatedInputTokens: 1000, Now: gateNow,
	})
	assert.False(t, sticky)
	assert.Empty(t, stay.Model)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, planner.ReasonNoPin, plan.Reason)
}

func TestHMMCostGateStayPinIgnoresNonHMMActivePin(t *testing.T) {
	_, ok := hmmGate().StayPin(router.Request{}, hmmPin("cluster_scorer", time.Minute), sessionpin.Pin{}, gateNow)
	assert.False(t, ok, "active pin not written by HMM")

	got, ok := hmmGate().StayPin(router.Request{}, hmmPin("hmm_policy", time.Minute), sessionpin.Pin{}, gateNow)
	require.True(t, ok)
	assert.Equal(t, stayModel, got.Model)
}

func TestHMMCostGateStayPinPrefersMostRecentTurn(t *testing.T) {
	older := hmmPin("hmm_policy", time.Hour)
	newer := hmmPin(hmm.HistoryReason, time.Minute)
	newer.Model = otherModel
	got, ok := hmmGate().StayPin(router.Request{}, older, newer, gateNow)
	require.True(t, ok)
	assert.Equal(t, otherModel, got.Model)
}

func TestHMMCostGateNormalizeStayPinRejectsIneligibleModels(t *testing.T) {
	pin := hmmPin("hmm_policy", time.Minute)
	unavailable := hmmGate()
	unavailable.AvailableModels = map[string]struct{}{otherModel: {}}
	cases := map[string]struct {
		gate hmm.CostGate
		req  router.Request
	}{
		"excluded":             {hmmGate(), router.Request{ExcludedModels: map[string]struct{}{stayModel: {}}}},
		"auto-disabled":        {hmmGate(), router.Request{AutomaticExcludedModels: map[string]struct{}{stayModel: {}}}},
		"unavailable":          {unavailable, router.Request{}},
		"provider not enabled": {hmmGate(), router.Request{EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, ok := tc.gate.NormalizeStayPin(tc.req, pin, gateNow)
			assert.False(t, ok)
		})
	}
	_, ok := hmmGate().NormalizeStayPin(router.Request{}, pin, gateNow)
	assert.True(t, ok, "eligible pin")
}

func TestHMMCostGateNormalizeStayPinRejectsStalePins(t *testing.T) {
	expired := hmmPin("hmm_policy", time.Minute)
	expired.PinnedUntil = gateNow.Add(-time.Second)
	noTurn := hmmPin("hmm_policy", 0)
	noTurn.LastTurnEndedAt = time.Time{}
	maxedOut := hmmPin("hmm_policy", time.Minute)
	maxedOut.LastOutputLimitAt = maxedOut.LastTurnEndedAt
	for name, pin := range map[string]sessionpin.Pin{"expired": expired, "no prior turn": noTurn, "maxed out": maxedOut} {
		t.Run(name, func(t *testing.T) {
			_, ok := hmmGate().NormalizeStayPin(router.Request{}, pin, gateNow)
			assert.False(t, ok)
		})
	}
}

func TestHMMCostGateNormalizeStayPinSplitsServedEffort(t *testing.T) {
	pin := hmmPin("hmm_policy", time.Minute)
	pin.LastServedModel = stayModel + ":high"
	got, ok := hmmGate().NormalizeStayPin(router.Request{}, pin, gateNow)
	require.True(t, ok)
	assert.Equal(t, stayModel, got.Model)
	assert.Equal(t, "high", got.Effort)
}

func TestHMMCostGateDecideSwitchesOnPricierPhaseChange(t *testing.T) {
	stay, plan, sticky := hmmGate().Decide(hmm.CostGateInput{
		ActivePin:            hmmPin("hmm_policy", time.Minute),
		Fresh:                router.Decision{Provider: providers.ProviderAnthropic, Model: otherModel, Reason: "hmm_policy:tool_execution"},
		EstimatedInputTokens: 1000,
		Now:                  gateNow,
	})
	assert.False(t, sticky)
	assert.Equal(t, stayModel, stay.Model)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, hmm.ReasonPhaseChange, plan.Reason)
}
