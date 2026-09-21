package router_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router"
)

func TestClassifierContextJoinsOnlyRecordedPredictions(t *testing.T) {
	input := router.ClassifierContext{
		TurnKey: "current", CurrentUserMessage: "next", CompletedResponseCount: 2,
		Features: router.ClassifierFeatures{UserMessageCount: 2, ToolCallCount: 12, ToolErrorCount: 3},
		PrecedingResponses: []router.ClassifierResponse{
			{ResponseIndex: 0, Content: "answer", TurnKey: "prior"},
			{ResponseIndex: 1, Content: "", TurnKey: "prior"},
		},
	}
	_, err := input.WithHistoricalPredictions(nil)
	require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
	request, err := input.WithHistoricalPredictions(map[string]router.ClassifierComplexity{"prior": router.ClassifierHigh})
	require.NoError(t, err)
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	require.JSONEq(t, `{"user":{"current_user_message":"next","preceding_agent_responses":[{"response_index":0,"content":"answer","complexity":2},{"response_index":1,"content":"","complexity":2}],"conversation_features":{"user_message_count":2,"tool_call_count":12,"tool_error_count":3}},"history_source":"historical_prediction","completed_response_count":2}`, string(encoded))
	_, err = input.WithHistoricalPredictions(map[string]router.ClassifierComplexity{"prior": 4})
	require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
	input.PrecedingResponses = input.PrecedingResponses[1:]
	_, err = input.WithHistoricalPredictions(map[string]router.ClassifierComplexity{"prior": router.ClassifierHigh})
	require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
}

func TestClassifierFirstTurnSerializesEmptyArray(t *testing.T) {
	input := router.ClassifierContext{TurnKey: "first", CurrentUserMessage: "hello", Features: router.ClassifierFeatures{UserMessageCount: 1}}
	request, err := input.WithHistoricalPredictions(nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	require.JSONEq(t, `{"user":{"current_user_message":"hello","preceding_agent_responses":[],"conversation_features":{"user_message_count":1,"tool_call_count":0,"tool_error_count":0}},"history_source":"historical_prediction","completed_response_count":0}`, string(encoded))
}

func TestClassifierContextRejectsInvalidProvenance(t *testing.T) {
	valid := router.ClassifierContext{TurnKey: "current", CompletedResponseCount: 1, Features: router.ClassifierFeatures{UserMessageCount: 2}, PrecedingResponses: []router.ClassifierResponse{{ResponseIndex: 0, TurnKey: "prior"}}}
	fixtures := map[string]func(*router.ClassifierContext){
		"no boundary":             func(c *router.ClassifierContext) { c.TurnKey = "" },
		"no user":                 func(c *router.ClassifierContext) { c.Features.UserMessageCount = 0 },
		"negative tools":          func(c *router.ClassifierContext) { c.Features.ToolCallCount = -1 },
		"errors exceed calls":     func(c *router.ClassifierContext) { c.Features.ToolErrorCount = 1 },
		"negative errors":         func(c *router.ClassifierContext) { c.Features.ToolErrorCount = -1 },
		"negative responses":      func(c *router.ClassifierContext) { c.CompletedResponseCount = -1 },
		"first user with history": func(c *router.ClassifierContext) { c.Features.UserMessageCount = 1 },
		"wrong ordinal":           func(c *router.ClassifierContext) { c.PrecedingResponses[0].ResponseIndex = 1 },
		"own response":            func(c *router.ClassifierContext) { c.PrecedingResponses[0].TurnKey = "current" },
	}
	for name, mutate := range fixtures {
		t.Run(name, func(t *testing.T) {
			input := valid
			input.PrecedingResponses = append([]router.ClassifierResponse(nil), valid.PrecedingResponses...)
			mutate(&input)
			_, err := input.WithHistoricalPredictions(map[string]router.ClassifierComplexity{"prior": router.ClassifierLow, "current": router.ClassifierLow})
			require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
		})
	}
}
