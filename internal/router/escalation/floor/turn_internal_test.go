package floor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
)

func TestApplyLLMMarksOnlyTheApplyingTurn(t *testing.T) {
	constrained := router.Decision{Metadata: &router.RoutingMetadata{Escalation: &escalation.Decision{Constrained: true}}}
	positive := func() llmescalation.Session {
		return llmescalation.Session{Pending: &llmescalation.Job{ID: "job", Judgment: &llmescalation.Judgment{Escalate: true}}}
	}
	withFloor := positive()
	withFloor.Floor = escalation.Maximum
	tests := []struct {
		name     string
		turn     *Turn
		decision router.Decision
		want     bool
	}{
		{name: "verdict applied this turn", turn: &Turn{llm: &llmTurn{active: true, session: positive()}}, decision: constrained, want: true},
		{name: "floor already in force", turn: &Turn{llm: &llmTurn{active: true, session: withFloor}}, decision: constrained, want: false},
		{name: "active verdict without a routing intervention", turn: &Turn{llm: &llmTurn{active: true, session: positive()}}, decision: router.Decision{}, want: false},
		{name: "no classifier", turn: nil, decision: constrained, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newTestService().WithLLMEscalation(&llmEscalationStoreStub{}, &blockingEscalationJudge{})
			require.NoError(t, service.ApplyLLM(context.Background(), tt.turn, tt.decision))
			assert.Equal(t, tt.want, tt.turn.LLMApplied())
		})
	}
}
