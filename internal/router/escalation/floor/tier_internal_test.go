package floor

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
)

func TestTurnFloor_HighestSourceWinsInEveryMode(t *testing.T) {
	turn := &Turn{
		xgb: &xgbTurn{session: escalation.Session{Floor: escalation.Medium}},
		llm: &llmTurn{session: llmescalation.Session{Floor: escalation.Maximum}},
	}
	assert.Equal(t, catalog.TierHigh, turn.Floor())
	assert.False(t, turn.Escalate())
	assert.False(t, turn.anyActive())
}

func TestTurnFloor_PositiveVerdictWithoutFloorEscalates(t *testing.T) {
	turn := &Turn{xgb: &xgbTurn{checkpoint: escalation.Checkpoint{Prediction: &escalation.Prediction{Escalate: true}}}}
	assert.Equal(t, catalog.TierUnknown, turn.Floor())
	assert.True(t, turn.Escalate())
}

func TestTurnFloor_ReplayedVerdictDoesNotEscalate(t *testing.T) {
	turn := &Turn{xgb: &xgbTurn{replay: true, checkpoint: escalation.Checkpoint{Prediction: &escalation.Prediction{Escalate: true}}}}
	assert.False(t, turn.Escalate())
	var none *Turn
	assert.Equal(t, catalog.TierUnknown, none.Floor())
}

func TestAtLeast(t *testing.T) {
	candidates := []string{"claude-haiku-4-5", "claude-opus-4-7", "definitely-not-a-model"}
	assert.Equal(t, []string{"claude-opus-4-7"}, AtLeast(candidates, catalog.TierHigh))
	assert.Equal(t, candidates, AtLeast(candidates, catalog.TierUnknown))
	assert.Empty(t, AtLeast(nil, catalog.TierLow))
}
