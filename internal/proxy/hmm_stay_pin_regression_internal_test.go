package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

func newHMMStayPinRegressionService() *Service {
	return withHMMPorts(NewService(
		nil,
		map[string]providers.Client{providers.ProviderAnthropic: nil, providers.ProviderMakora: nil},
		nil,
		nil,
		false,
		"anthropic", "claude-haiku-4-5",
		nil))

}

func hmmStayPinRegressionFresh(reason string) router.Decision {
	return router.Decision{
		Provider: providers.ProviderMakora,
		Model:    "deepseek/deepseek-v4.1-flash",
		Reason:   reason,
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}
}

func TestHMMCostGate_EffortQualifiedHistoryStaysOnWarmCache(t *testing.T) {
	svc := newHMMStayPinRegressionService()
	history := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		LastServedModel: "claude-sonnet-5:high",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}

	decision, plan, sticky, stayModel := hmmCostGatedDecision(t.Context(), svc, hmm.CostGateInput{
		Request: router.Request{}, ActivePin: sessionpin.Pin{}, HMMHistory: history, Fresh: hmmStayPinRegressionFresh("hmm_policy(classifier 'fast')"),
		EstimatedInputTokens: 100, PrefixBroken: false,
	})

	assert.True(t, sticky, "an effort-qualified served identity must stay usable as the HMM stay pin")
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Equal(t, "high", decision.Effort, "staying must keep the effort the session was served at")
	assert.Equal(t, "claude-sonnet-5:high", decision.ServedIdentity())
	assert.Equal(t, providers.ProviderAnthropic, decision.Provider)
	assert.Equal(t, "claude-sonnet-5", stayModel)
	assert.Equal(t, planner.OutcomeStay, plan.Outcome)
	assert.Equal(t, planner.ReasonEVNegative, plan.Reason)
}

func TestHMMCostGate_EffortQualifiedHistoryIsPricedByEVGate(t *testing.T) {
	svc := newHMMStayPinRegressionService()
	history := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		LastServedModel: "claude-sonnet-5:high",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}

	decision, plan, sticky, stayModel := hmmCostGatedDecision(t.Context(), svc, hmm.CostGateInput{
		Request: router.Request{}, ActivePin: sessionpin.Pin{}, HMMHistory: history, Fresh: hmmStayPinRegressionFresh("hmm_policy(classifier 'fast')"),
		EstimatedInputTokens: 10_000, PrefixBroken: false,
	})

	assert.False(t, sticky)
	assert.Equal(t, "deepseek/deepseek-v4.1-flash", decision.Model)
	assert.Equal(t, "claude-sonnet-5", stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, planner.ReasonEVPositive, plan.Reason, "the switch must come from the EV gate, not a missing pin")
}

func TestHMMCostGate_PhaseChangeStaysWhenEVDoesNotCoverWarmCache(t *testing.T) {
	svc := newHMMStayPinRegressionService()
	history := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		LastServedModel: "claude-sonnet-5",
		Reason:          "hmm_policy:tool_execution(label=explore)",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
		PinnedUntil:     time.Now().Add(time.Hour),
	}

	decision, plan, sticky, stayModel := hmmCostGatedDecision(t.Context(), svc, hmm.CostGateInput{
		Request: router.Request{}, ActivePin: sessionpin.Pin{}, HMMHistory: history, Fresh: hmmStayPinRegressionFresh("hmm_policy(classifier 'balanced')"),
		EstimatedInputTokens: 100, PrefixBroken: false,
	})

	assert.True(t, sticky, "a phase change must not evict a warm cache the EV gate says is worth keeping")
	assert.Equal(t, "claude-sonnet-5", decision.Model)
	assert.Equal(t, "claude-sonnet-5", stayModel)
	assert.Equal(t, planner.OutcomeStay, plan.Outcome)
	assert.Equal(t, planner.ReasonEVNegative, plan.Reason)
	assert.True(t, plan.ShadowComputed, "phase changes must be priced by the planner")
}

func TestHMMCostGate_PhaseChangeSwitchRecordsEvictionCost(t *testing.T) {
	svc := newHMMStayPinRegressionService()
	history := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		LastServedModel: "claude-sonnet-5",
		Reason:          "hmm_policy:tool_execution(label=explore)",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
		PinnedUntil:     time.Now().Add(time.Hour),
	}

	decision, plan, sticky, _ := hmmCostGatedDecision(t.Context(), svc, hmm.CostGateInput{
		Request: router.Request{}, ActivePin: sessionpin.Pin{}, HMMHistory: history, Fresh: hmmStayPinRegressionFresh("hmm_policy(classifier 'balanced')"),
		EstimatedInputTokens: 10_000, PrefixBroken: false,
	})

	assert.False(t, sticky)
	assert.Equal(t, "deepseek/deepseek-v4.1-flash", decision.Model)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, hmm.ReasonPhaseChange, plan.Reason)
	assert.Positive(t, plan.EvictionCostUSD, "a warm-cache phase-change switch must record the eviction it pays")
}

func TestHMMCostGate_PhaseChangeUpgradeFollowsFreshAtLowConfidence(t *testing.T) {
	svc := newHMMStayPinRegressionService()
	history := sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		LastServedModel: "claude-haiku-4-5",
		Reason:          "hmm_policy:tool_execution(label=explore)",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
		PinnedUntil:     time.Now().Add(time.Hour),
	}
	fresh := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-opus-4-7",
		Reason:   "hmm_policy(classifier 'high')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.30,
		},
	}

	decision, plan, sticky, _ := hmmCostGatedDecision(t.Context(), svc, hmm.CostGateInput{
		Request: router.Request{}, ActivePin: sessionpin.Pin{}, HMMHistory: history, Fresh: fresh,
		EstimatedInputTokens: 10_000, PrefixBroken: false,
	})

	assert.False(t, sticky, "leaving tool execution must not trap the main loop on the cheaper explore model")
	assert.Equal(t, "claude-opus-4-7", decision.Model)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, hmm.ReasonPhaseChange, plan.Reason)
	assert.True(t, plan.ShadowComputed, "the switch still records the planner's cost evidence")
}
