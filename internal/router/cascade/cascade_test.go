package cascade_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router/cascade"
)

func ladder() cascade.Ladder {
	return cascade.Ladder{
		{Provider: providers.ProviderFireworks, Model: "deepseek/deepseek-v4-flash"},
		{Provider: providers.ProviderAnthropic, Model: "claude-opus-5"},
	}
}

func TestOneFailureDoesNotEscalate(t *testing.T) {
	// One failure is the agent loop working: write, test, see red, fix. A cascade
	// that escalates here collapses into "always the strong model".
	state := cascade.State{}.Observe(cascade.Failed)

	assert.False(t, state.Escalated)
	rung, index, ok := ladder().Select(state)
	require.True(t, ok)
	assert.Equal(t, 0, index)
	assert.Equal(t, "deepseek/deepseek-v4-flash", rung.Model)
}

func TestTwoConsecutiveFailuresEscalate(t *testing.T) {
	state := cascade.State{}.Observe(cascade.Failed).Observe(cascade.Failed)

	assert.True(t, state.Escalated)
	rung, index, ok := ladder().Select(state)
	require.True(t, ok)
	assert.Equal(t, 1, index)
	assert.Equal(t, "claude-opus-5", rung.Model)
}

func TestAPassResetsTheFailureCount(t *testing.T) {
	state := cascade.State{}.
		Observe(cascade.Failed).
		Observe(cascade.Passed).
		Observe(cascade.Failed)

	assert.False(t, state.Escalated, "failures must be consecutive")
	assert.Equal(t, 1, state.ConsecutiveFailures)
}

func TestNoSignalIsInert(t *testing.T) {
	// Most turns carry no test output. A long run of them must neither escalate a
	// session nor forgive one that is genuinely stuck.
	state := cascade.State{}.Observe(cascade.Failed)
	for range 10 {
		state = state.Observe(cascade.NoSignal)
	}
	assert.Equal(t, 1, state.ConsecutiveFailures)
	assert.False(t, state.Escalated)

	assert.True(t, state.Observe(cascade.Failed).Escalated)
}

func TestEscalationIsSticky(t *testing.T) {
	// A session that needed help at turn 20 is a hard session; one green test
	// does not change that, and de-escalating would re-pay the prompt-cache
	// forfeit on every flip.
	state := cascade.State{}.Observe(cascade.Failed).Observe(cascade.Failed)
	state = state.Advance(1)

	for range 5 {
		state = state.Observe(cascade.Passed)
	}
	_, index, ok := ladder().Select(state)
	require.True(t, ok)
	assert.Equal(t, 1, index, "must not fall back to the cheap rung")
}

func TestAdvanceSpendsTheEscalationSoOneFailurePairIsOneStep(t *testing.T) {
	// Without this, a single escalation would climb the whole ladder as every
	// later Select saw the flag still set.
	long := cascade.Ladder{
		{Model: "cheap"}, {Model: "mid"}, {Model: "dear"},
	}
	state := cascade.State{}.Observe(cascade.Failed).Observe(cascade.Failed)

	_, index, _ := long.Select(state)
	require.Equal(t, 1, index)
	state = state.Advance(index)

	_, index, _ = long.Select(state)
	assert.Equal(t, 1, index, "still on mid until two more failures")

	state = state.Observe(cascade.Failed).Observe(cascade.Failed)
	_, index, _ = long.Select(state)
	assert.Equal(t, 2, index)
}

func TestTheLadderIsCapped(t *testing.T) {
	// Beyond MaxRungs the tail pays for another full attempt to rescue a session
	// that several models already failed.
	long := cascade.Ladder{
		{Model: "a"}, {Model: "b"}, {Model: "c"}, {Model: "d"}, {Model: "e"},
	}
	state := cascade.State{Rung: cascade.MaxRungs - 1, Escalated: true}

	rung, index, ok := long.Select(state)
	require.True(t, ok)
	assert.Equal(t, cascade.MaxRungs-1, index)
	assert.Equal(t, "c", rung.Model)
}

func TestASingleRungLadderIsInertNotBroken(t *testing.T) {
	// What the Pareto gate produced on one of four offline panels: the cheapest
	// arm was also the best, so there was nothing to escalate to. That is a fact
	// about the arm set, not a failure.
	one := cascade.Ladder{{Model: "only"}}
	state := cascade.State{}.Observe(cascade.Failed).Observe(cascade.Failed)

	rung, index, ok := one.Select(state)
	require.True(t, ok)
	assert.Equal(t, 0, index)
	assert.Equal(t, "only", rung.Model)
}

func TestAnEmptyLadderIsUnavailableRatherThanAGuess(t *testing.T) {
	_, _, ok := cascade.Ladder{}.Select(cascade.State{})
	assert.False(t, ok, "a misconfigured ladder must not silently pick a model")
}
