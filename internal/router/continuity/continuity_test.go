package continuity_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/planner"
)

func pinned() router.SessionView {
	return router.SessionView{Pin: router.SessionPin{Found: true, Provider: "anthropic", Model: "claude-sonnet-4-5"}}
}

func TestToolResultStickyRequiresEligiblePinAndToolResult(t *testing.T) {
	turn := continuity.Turn{IsToolResult: true}
	assert.True(t, continuity.ToolResultSticky(pinned(), turn, continuity.Config{}))
	assert.True(t, continuity.ToolResultSticky(pinned(), turn, continuity.Config{ScoreToolResultTurns: true, HoldToolResult: true}))
	assert.False(t, continuity.ToolResultSticky(pinned(), turn, continuity.Config{ScoreToolResultTurns: true}))
	assert.False(t, continuity.ToolResultSticky(router.SessionView{}, turn, continuity.Config{}))
	assert.False(t, continuity.ToolResultSticky(pinned(), continuity.Turn{}, continuity.Config{}))
	assert.False(t, continuity.ToolResultSticky(pinned(), continuity.Turn{IsToolResult: true, Escalated: true}, continuity.Config{}))
	assert.False(t, continuity.ToolResultSticky(pinned(), continuity.Turn{IsToolResult: true, AuthoritativePerTurn: true}, continuity.Config{}))
}

func TestCanReusePinOnlyWhenPlannerDisabled(t *testing.T) {
	assert.True(t, continuity.CanReusePin(pinned(), continuity.Turn{}, continuity.Config{}))
	assert.False(t, continuity.CanReusePin(pinned(), continuity.Turn{}, continuity.Config{PlannerEnabled: true}))
	assert.False(t, continuity.CanReusePin(router.SessionView{}, continuity.Turn{}, continuity.Config{}))
	assert.False(t, continuity.CanReusePin(pinned(), continuity.Turn{Escalated: true}, continuity.Config{}))
	assert.False(t, continuity.CanReusePin(pinned(), continuity.Turn{AuthoritativePerTurn: true}, continuity.Config{}))
}

func TestPlanIgnoresUnfoundPin(t *testing.T) {
	session := router.SessionView{Pin: router.SessionPin{Model: "claude-sonnet-4-5", LastTurnEndedAt: time.Now()}}
	got := continuity.Plan(session, router.Decision{Model: "claude-sonnet-4-5"}, continuity.PlanInputs{PinCacheCold: true}, planner.EVConfig{})
	assert.Equal(t, planner.OutcomeSwitch, got.Outcome)
	assert.Equal(t, planner.ReasonNoPin, got.Reason)
}

func TestPlanStaysOnSameModel(t *testing.T) {
	got := continuity.Plan(pinned(), router.Decision{Model: "claude-sonnet-4-5"}, continuity.PlanInputs{}, planner.EVConfig{})
	assert.Equal(t, planner.OutcomeStay, got.Outcome)
	assert.Equal(t, planner.ReasonSameModel, got.Reason)
}

func plannedSession(outputTokens int) router.SessionView {
	return router.SessionView{Pin: router.SessionPin{
		Found:            true,
		Provider:         providers.ProviderAnthropic,
		Model:            "claude-opus-4-7",
		LastTurnEndedAt:  time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
		LastOutputTokens: outputTokens,
	}}
}

var (
	haikuFresh   = router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5"}
	plannedModel = map[string]struct{}{"claude-opus-4-7": {}, "claude-haiku-4-5": {}}
)

func TestPlanPricesThePinAtItsNamedProvider(t *testing.T) {
	got := continuity.Plan(plannedSession(0), haikuFresh, continuity.PlanInputs{AvailableModels: plannedModel}, planner.EVConfig{ExpectedRemainingTurns: 1})
	require.True(t, got.ShadowComputed)
	assert.False(t, got.PinPriceFallback)
}

func TestPlanForwardsTheInputTokenEstimate(t *testing.T) {
	in := continuity.PlanInputs{EstimatedInputTokens: 100_000, PinCacheCold: true, AvailableModels: plannedModel}
	got := continuity.Plan(plannedSession(0), haikuFresh, in, planner.EVConfig{ExpectedRemainingTurns: 1})
	assert.InDelta(t, 0.4, got.ExpectedSavingsUSD, 1e-9)
}

func TestPlanCountsThePinsPriorOutputTokens(t *testing.T) {
	cfg := planner.EVConfig{ExpectedRemainingTurns: 1, CorrectedEconomics: true}
	in := continuity.PlanInputs{CachePrefixKnown: true, AvailableModels: plannedModel}
	assert.InDelta(t, 20.0, continuity.Plan(plannedSession(1_000_000), haikuFresh, in, cfg).ExpectedSavingsUSD, 1e-9)
	assert.InDelta(t, 0.0, continuity.Plan(plannedSession(0), haikuFresh, in, cfg).ExpectedSavingsUSD, 1e-9)
}

func TestPlanForwardsTheCacheablePrefix(t *testing.T) {
	cfg := planner.EVConfig{ExpectedRemainingTurns: 1, CorrectedEconomics: true}
	in := continuity.PlanInputs{EstimatedInputTokens: 100_000, CacheablePrefixTokens: 50_000, CachePrefixKnown: true, AvailableModels: plannedModel}
	assert.InDelta(t, 0.22, continuity.Plan(plannedSession(0), haikuFresh, in, cfg).ExpectedSavingsUSD, 1e-9)
}

func TestPlanPassesTheColdPinFlagForAFoundPin(t *testing.T) {
	cfg := planner.EVConfig{ExpectedRemainingTurns: 1}
	in := continuity.PlanInputs{EstimatedInputTokens: 100_000, AvailableModels: plannedModel}
	warm := continuity.Plan(plannedSession(0), haikuFresh, in, cfg)
	in.PinCacheCold = true
	cold := continuity.Plan(plannedSession(0), haikuFresh, in, cfg)
	assert.False(t, warm.PinCacheCold)
	assert.True(t, cold.PinCacheCold)
	assert.Greater(t, warm.EvictionCostUSD, 0.0)
	assert.Zero(t, cold.EvictionCostUSD)
}

func TestPlanForwardsAvailableModels(t *testing.T) {
	in := continuity.PlanInputs{AvailableModels: map[string]struct{}{"claude-haiku-4-5": {}}}
	got := continuity.Plan(plannedSession(0), haikuFresh, in, planner.EVConfig{})
	assert.Equal(t, planner.ReasonPinModelMissing, got.Reason)
}
