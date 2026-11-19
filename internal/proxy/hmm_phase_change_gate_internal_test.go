package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

func TestHMMPhaseChangeGate_HoldsServedModelUntilSecondVote(t *testing.T) {
	svc := NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderMakora: nil},
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	)
	activePin := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           "claude-sonnet-5",
		LastServedModel: "claude-sonnet-5",
		Reason:          "hmm_policy:tool_execution(label=explore)",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
		PinnedUntil:     time.Now().Add(time.Hour),
	}
	input := routingpolicy.HMMCostGateInput{
		Request: router.Request{}, ActivePin: activePin, HMMHistory: sessionpin.Pin{},
		Fresh: router.Decision{
			Provider: providers.ProviderMakora,
			Model:    "deepseek/deepseek-v4.1-flash",
			Reason:   "hmm_policy(classifier 'balanced')",
			Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMM), RouteID: "route-1", ChosenScore: 0.70},
		},
		EstimatedInputTokens: 10_000, PrefixBroken: false,
	}

	off, offResult := hmmCostGatedResult(t.Context(), svc, input)
	assert.Equal(t, "deepseek/deepseek-v4.1-flash", off.Model)
	assert.Equal(t, routingpolicy.HMMReasonPhaseChange, offResult.Plan.Reason)

	on := flags.WithOverrides(t.Context(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyHMMPhaseChangeGate: true}})
	first, firstResult := hmmCostGatedResult(on, svc, input)
	assert.Equal(t, "claude-sonnet-5", first.Model)
	assert.True(t, firstResult.Sticky)
	assert.Equal(t, planner.OutcomeStay, firstResult.Plan.Outcome)
	assert.Equal(t, routingpolicy.HMMReasonPhaseChangeDwell, firstResult.Plan.Reason)
	assert.Equal(t, 1, firstResult.DowngradeVotes)

	input.ActivePin.Reason = input.Fresh.Reason
	input.ActivePin.ConsecutiveDowngradeVotes = firstResult.DowngradeVotes
	second, secondResult := hmmCostGatedResult(on, svc, input)
	assert.Equal(t, "deepseek/deepseek-v4.1-flash", second.Model)
	assert.False(t, secondResult.Sticky)
	assert.Equal(t, routingpolicy.HMMReasonPhaseChange, secondResult.Plan.Reason)
	assert.Zero(t, secondResult.DowngradeVotes)
}
