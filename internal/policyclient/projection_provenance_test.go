package policyclient

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func TestProjectionKeepsOriginalTurnProvenance(t *testing.T) {
	messages := []router.ConversationMessage{{Role: "user", Text: "Original task"}}
	for i := 0; i < 60; i++ {
		messages = append(messages,
			router.ConversationMessage{Role: "assistant", Text: "Tool call context"},
			router.ConversationMessage{Role: "user", ToolResults: []router.ConversationToolResult{{ToolUseID: "call", Text: "real result", ResultPresent: true}}})
	}
	body, err := marshalRouteRequest(policy.Query{
		Strategy:             router.StrategyHMMEmbedding,
		SchemaVersion:        policy.SchemaVersionV3,
		ConversationMessages: messages,
		TrainingAllowed:      true,
		TurnContext:          &router.PolicyTurnContext{VisibleTurnIndex: 60, HistoryTruncated: true},
	})
	require.NoError(t, err)
	var got routeRequest
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "Original task", got.LatestUserText)
	require.NotNil(t, got.VisibleTurnIndex)
	assert.Equal(t, 60, *got.VisibleTurnIndex)
	require.NotNil(t, got.HistoryTruncated)
	assert.True(t, *got.HistoryTruncated)
	require.Len(t, got.TrainingConversationDelta, 2)
	assert.Equal(t, "real result", got.TrainingConversationDelta[1].ToolResults[0].Text)
}
