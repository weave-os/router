package continuity_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/planner"
)

func pinned() router.SessionView {
	return router.SessionView{Pin: router.SessionPin{Found: true, Provider: "anthropic", Model: "claude-sonnet-4-5"}}
}

func TestToolResultStickyRequiresEligiblePinAndToolResult(t *testing.T) {
	turn := continuity.Turn{ToolResult: true}
	assert.True(t, continuity.ToolResultSticky(pinned(), turn, continuity.Config{}))
	assert.True(t, continuity.ToolResultSticky(pinned(), turn, continuity.Config{ScoreToolResultTurns: true, HoldToolResult: true}))
	assert.False(t, continuity.ToolResultSticky(pinned(), turn, continuity.Config{ScoreToolResultTurns: true}))
	assert.False(t, continuity.ToolResultSticky(router.SessionView{}, turn, continuity.Config{}))
	assert.False(t, continuity.ToolResultSticky(pinned(), continuity.Turn{}, continuity.Config{}))
	assert.False(t, continuity.ToolResultSticky(pinned(), continuity.Turn{ToolResult: true, Escalated: true}, continuity.Config{}))
	assert.False(t, continuity.ToolResultSticky(pinned(), continuity.Turn{ToolResult: true, AuthoritativePerTurn: true}, continuity.Config{}))
}

func TestReuseOnlyWhenPlannerDisabled(t *testing.T) {
	assert.True(t, continuity.Reuse(pinned(), continuity.Turn{}, continuity.Config{}))
	assert.False(t, continuity.Reuse(pinned(), continuity.Turn{}, continuity.Config{PlannerEnabled: true}))
	assert.False(t, continuity.Reuse(router.SessionView{}, continuity.Turn{}, continuity.Config{}))
	assert.False(t, continuity.Reuse(pinned(), continuity.Turn{Escalated: true}, continuity.Config{}))
	assert.False(t, continuity.Reuse(pinned(), continuity.Turn{AuthoritativePerTurn: true}, continuity.Config{}))
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
