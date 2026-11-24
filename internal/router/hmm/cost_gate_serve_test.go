package hmm_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/planner"
)

func TestHMMCostGateServeWithoutStayPinServesFreshUnpinned(t *testing.T) {
	fresh := router.Decision{Provider: providers.ProviderAnthropic, Model: otherModel}
	served := hmmGate().Serve(costGateServeInput(fresh), "")
	assert.Equal(t, fresh, served.Decision)
	assert.False(t, served.Sticky)
	assert.Empty(t, served.StayModel)
	assert.Equal(t, planner.ReasonNoPin, served.Planner.Reason)
	assert.Equal(t, "hmm_fresh_unpinned", served.PinTier)
	assert.Empty(t, served.HeldEffort)
}

func TestHMMCostGateServeHoldsIncumbentEffortAndKeepsTierReason(t *testing.T) {
	fresh := hysteresisDecision(map[string]float32{
		"anthropic/claude-opus-5:low":  4.0,
		"anthropic/claude-opus-5:high": 4.5,
	})
	fresh.Effort = "high"
	served := hmmGate().Serve(costGateServeInput(fresh), "claude-opus-5:low")
	assert.Equal(t, "low", served.Decision.Effort)
	assert.Equal(t, "high", served.ChallengerEffort)
	assert.Equal(t, "low", served.HeldEffort)
	assert.Equal(t, planner.ReasonNoPin+"+effort_hysteresis", served.Planner.Reason)
	assert.Equal(t, "hmm_fresh_unpinned", served.PinTier)
}

func costGateServeInput(fresh router.Decision) hmm.CostGateInput {
	return hmm.CostGateInput{Fresh: fresh, EstimatedInputTokens: 1000, Now: gateNow}
}

func TestHMMCostGateServeReportsTheUpgradeConfidenceVerdict(t *testing.T) {
	gate := hmmGate()
	gate.UpgradeConfidenceThreshold = 0.5
	scored := func(score float32, reason string) router.Decision {
		return router.Decision{Provider: providers.ProviderAnthropic, Model: otherModel, Reason: reason, Metadata: &router.RoutingMetadata{ChosenScore: score}}
	}
	cases := []struct {
		name       string
		fresh      router.Decision
		confidence float64
		low        bool
	}{
		{"score below the threshold is low confidence", scored(0.3, "hmm_policy"), 0.3, true},
		{"score at the threshold is confident", scored(0.5, "hmm_policy"), 0.5, false},
		{"missing score is low confidence", router.Decision{Provider: providers.ProviderAnthropic, Model: otherModel, Reason: "hmm_policy"}, 0, true},
		{"phase change upgrades regardless of the score", scored(0.3, "hmm_policy:tool_execution"), 0.3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := costGateServeInput(tc.fresh)
			input.ActivePin = hmmPin("hmm_policy", time.Minute)
			served := gate.Serve(input, "")
			assert.InDelta(t, tc.confidence, served.UpgradeConfidence, 1e-6)
			assert.Equal(t, tc.low, served.UpgradeConfidenceLow)
		})
	}
}
