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
	stay, plan, sticky := hmmGate().Decide(routingpolicy.HMMCostGateInput{
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
	newer := hmmPin(routingpolicy.HMMHistoryReason, time.Minute)
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

func TestBaseModelOfStripsEffort(t *testing.T) {
	gpt := string(catalog.ModelIDGPT55)
	assert.Equal(t, gpt, routingpolicy.BaseModelOf(gpt+":high"))
	assert.Equal(t, stayModel, routingpolicy.BaseModelOf(stayModel))
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
	stay, plan, sticky := hmmGate().Decide(routingpolicy.HMMCostGateInput{
		ActivePin:            hmmPin("hmm_policy", time.Minute),
		Fresh:                router.Decision{Provider: providers.ProviderAnthropic, Model: otherModel, Reason: "hmm_policy:tool_execution"},
		EstimatedInputTokens: 1000,
		Now:                  gateNow,
	})
	assert.False(t, sticky)
	assert.Equal(t, stayModel, stay.Model)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, routingpolicy.HMMReasonPhaseChange, plan.Reason)
}

func phaseGate() routingpolicy.HMMCostGate {
	gate := hmmGate()
	gate.UpgradeConfidenceThreshold = 0.5
	return gate
}

func phaseGateInput(pin sessionpin.Pin, freshModel, freshReason string, confidence float32) routingpolicy.HMMCostGateInput {
	return routingpolicy.HMMCostGateInput{
		ActivePin: pin,
		Fresh: router.Decision{
			Provider: providers.ProviderAnthropic, Model: freshModel, Reason: freshReason,
			Metadata: &router.RoutingMetadata{ChosenScore: confidence},
		},
		EstimatedInputTokens: 1000,
		Planner:              planner.EVConfig{ExpectedRemainingTurns: 3},
		PhaseChangeGate:      true,
		Now:                  gateNow,
	}
}

func TestHMMCostGatePhaseChangeGateHoldsLowConfidenceUpgrade(t *testing.T) {
	result := phaseGate().DecideWithVotes(phaseGateInput(hmmPin("hmm_policy:tool_execution", time.Minute), otherModel, "hmm_policy", 0.4))
	assert.True(t, result.Sticky)
	assert.Equal(t, planner.OutcomeStay, result.Plan.Outcome)
	assert.Equal(t, routingpolicy.HMMReasonPhaseChangeConfidenceLow, result.Plan.Reason)
	assert.Zero(t, result.UpgradeVotes)
}

func TestHMMCostGatePhaseChangeGateHoldsFirstConfidentUpgradeVote(t *testing.T) {
	result := phaseGate().DecideWithVotes(phaseGateInput(hmmPin("hmm_policy:tool_execution", time.Minute), otherModel, "hmm_policy", 0.7))
	assert.True(t, result.Sticky)
	assert.Equal(t, planner.OutcomeStay, result.Plan.Outcome)
	assert.Equal(t, routingpolicy.HMMReasonPhaseChangeDwell, result.Plan.Reason)
	assert.Equal(t, 1, result.UpgradeVotes)
}

func TestHMMCostGatePhaseChangeGateSwitchesOnSecondUpgradeVote(t *testing.T) {
	pin := hmmPin("hmm_policy", time.Minute)
	pin.ConsecutiveUpgradeVotes = 1
	result := phaseGate().DecideWithVotes(phaseGateInput(pin, otherModel, "hmm_policy", 0.7))
	assert.False(t, result.Sticky)
	assert.Equal(t, planner.OutcomeSwitch, result.Plan.Outcome)
	assert.Equal(t, routingpolicy.HMMReasonPhaseChange, result.Plan.Reason)
	assert.Zero(t, result.UpgradeVotes)
}

func TestHMMCostGatePhaseChangeGateHoldsDowngradeUntilSecondVote(t *testing.T) {
	pin := hmmPin("hmm_policy", 2*time.Hour)
	pin.Model = otherModel
	pin.LastInputTokens, pin.LastOutputTokens = 1000, 4000

	baseline := phaseGateInput(pin, stayModel, "hmm_policy:tool_execution", 0.7)
	baseline.PhaseChangeGate = false
	want := phaseGate().DecideWithVotes(baseline)
	require.Equal(t, planner.OutcomeSwitch, want.Plan.Outcome)

	lowConfidence := phaseGate().DecideWithVotes(phaseGateInput(pin, stayModel, "hmm_policy:tool_execution", 0.4))
	assert.True(t, lowConfidence.Sticky)
	assert.Equal(t, routingpolicy.HMMReasonPhaseChangeConfidenceLow, lowConfidence.Plan.Reason)
	assert.Zero(t, lowConfidence.DowngradeVotes)

	first := phaseGate().DecideWithVotes(phaseGateInput(pin, stayModel, "hmm_policy:tool_execution", 0.7))
	assert.True(t, first.Sticky)
	assert.Equal(t, routingpolicy.HMMReasonPhaseChangeDwell, first.Plan.Reason)
	assert.Equal(t, 1, first.DowngradeVotes)

	pin.ConsecutiveDowngradeVotes = 1
	pin.Reason = "hmm_policy:tool_execution"
	second := phaseGate().DecideWithVotes(phaseGateInput(pin, stayModel, "hmm_policy:tool_execution", 0.7))
	assert.False(t, second.Sticky)
	assert.Equal(t, planner.OutcomeSwitch, second.Plan.Outcome)
	assert.Zero(t, second.DowngradeVotes)
}

func TestHMMCostGatePhaseChangeGateOffKeepsForcedUpgrade(t *testing.T) {
	input := phaseGateInput(hmmPin("hmm_policy:tool_execution", time.Minute), otherModel, "hmm_policy", 0.4)
	input.PhaseChangeGate = false
	result := phaseGate().DecideWithVotes(input)
	assert.False(t, result.Sticky)
	assert.Equal(t, routingpolicy.HMMReasonPhaseChange, result.Plan.Reason)
}
