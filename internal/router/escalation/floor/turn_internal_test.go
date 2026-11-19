package floor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
)

func TestLLMAppliedMarksOnlyTheApplyingTurn(t *testing.T) {
	tests := []struct {
		name string
		turn *Turn
		want bool
	}{
		{name: "verdict applied this turn", turn: &Turn{llm: &llmTurn{active: true, applied: true}}, want: true},
		{name: "floor already in force", turn: &Turn{llm: &llmTurn{active: true, session: llmescalation.Session{Floor: escalation.Maximum}}}, want: false},
		{name: "no classifier", turn: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.turn.LLMApplied())
		})
	}
}
