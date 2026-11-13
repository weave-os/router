package decisionmodel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStaleStoreKeepsEscalationAndTask(t *testing.T) {
	decisionRouter := New(nil)
	decisionRouter.store("session", sessionState{taskComplexity: ChoiceLow, escalated: true})
	decisionRouter.store("session", sessionState{taskComplexity: ChoiceHigh})
	require.Equal(t, sessionState{taskComplexity: ChoiceLow, escalated: true}, decisionRouter.load("session"))
}
