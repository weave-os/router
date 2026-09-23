package proxy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func TestClassifierContextProtocolParity(t *testing.T) {
	fixtures := []struct {
		name  string
		parse func([]byte) (*translate.RequestEnvelope, error)
		body  string
	}{
		{"anthropic", translate.ParseAnthropic, `{"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"  answer  "},{"type":"text","text":""},{"type":"tool_use","id":"c1","name":"test","input":{"secret":"arguments"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","is_error":true,"content":"private output"}]},
			{"role":"user","content":"second"}]}`},
		{"chat", translate.ParseOpenAI, `{"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","reasoning_content":"private reasoning","content":[{"type":"text","text":"  answer  "},{"type":"text","text":""}],"tool_calls":[{"id":"c1","type":"function","function":{"name":"test","arguments":"{\"secret\":\"arguments\"}"}}]},
			{"role":"tool","tool_call_id":"c1","is_error":true,"content":"private output"},
			{"role":"user","content":"second"}]}`},
		{"responses", translate.ParseOpenAI, `{"model":"auto","input":[
			{"role":"user","content":"first"},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"private reasoning"}]},
			{"role":"assistant","content":[{"type":"output_text","text":"  answer  "},{"type":"output_text","text":""}]},
			{"type":"function_call","call_id":"c1","name":"test","arguments":"{\"secret\":\"arguments\"}"},
			{"type":"function_call_output","call_id":"c1","status":"failed","output":"private output"},
			{"role":"user","content":"second"}]}`},
		{"gemini", translate.ParseGemini, `{"contents":[
			{"role":"user","parts":[{"text":"first"}]},
			{"role":"model","parts":[{"thought":true,"text":"private reasoning"},{"text":"  answer  "},{"text":""},{"functionCall":{"id":"c1","name":"test","args":{"secret":"arguments"}}}]},
			{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"test","response":{"error":"private output"}}}]},
			{"role":"user","parts":[{"text":"second"}]}]}`},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			envelope, err := fixture.parse([]byte(fixture.body))
			require.NoError(t, err)
			observation, err := envelope.EscalationObservation()
			require.NoError(t, err)
			input, err := classifierContextForCall(observation)
			require.NoError(t, err)
			require.Equal(t, "second", input.CurrentUserMessage)
			require.Equal(t, router.ClassifierFeatures{UserMessageCount: 2, ToolCallCount: 1, ToolErrorCount: 1}, input.Features)
			require.Equal(t, 2, input.CompletedResponseCount)
			require.Len(t, input.PrecedingResponses, 2)
			require.Equal(t, "  answer  ", input.PrecedingResponses[0].Content)
			require.Equal(t, "", input.PrecedingResponses[1].Content)
			require.Equal(t, 1, input.PrecedingResponses[1].ResponseIndex)
			require.Equal(t, input.PrecedingResponses[0].PrefixDigest, input.PrecedingResponses[1].PrefixDigest)
			require.NotEqual(t, input.TurnDigest, input.PrecedingResponses[0].PrefixDigest)
		})
	}
}

func classifierTestObservation(messages ...translate.EscalationMessage) translate.EscalationObservation {
	return translate.EscalationObservation{HistoryComplete: true, Messages: messages}
}

func classifierTestText(role translate.EscalationRole, text string) translate.EscalationMessage {
	return translate.EscalationMessage{Role: role, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: text}}}
}

func TestClassifierContextWindowAndWholePrefixCounters(t *testing.T) {
	messages := []translate.EscalationMessage{classifierTestText(translate.EscalationRoleUser, "first")}
	failed := true
	for index := range 14 {
		callID := fmt.Sprintf("call-%d", index)
		messages = append(messages,
			translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{
				{Type: translate.EscalationBlockText, Text: fmt.Sprintf("answer-%d", index)},
				{Type: translate.EscalationBlockToolCall, ID: callID, Name: "test"},
			}},
			translate.EscalationMessage{Role: translate.EscalationRoleTool, Blocks: []translate.EscalationBlock{
				{Type: translate.EscalationBlockToolResult, CallID: callID, IsError: &failed},
			}},
		)
	}
	messages = append(messages, classifierTestText(translate.EscalationRoleUser, "next"))
	input, err := classifierContextForCall(classifierTestObservation(messages...))
	require.NoError(t, err)
	require.Equal(t, 14, input.CompletedResponseCount)
	require.Equal(t, router.ClassifierFeatures{UserMessageCount: 2, ToolCallCount: 14, ToolErrorCount: 14}, input.Features)
	require.Len(t, input.PrecedingResponses, 10)
	for index, response := range input.PrecedingResponses {
		require.Equal(t, index+4, response.ResponseIndex)
		require.Equal(t, fmt.Sprintf("answer-%d", index+4), response.Content)
	}
}

func TestClassifierContextRetriesReuseCallButToolLoopsRefresh(t *testing.T) {
	first := classifierTestText(translate.EscalationRoleUser, "first")
	initial, err := classifierContextForCall(classifierTestObservation(first))
	require.NoError(t, err)
	loop := classifierTestObservation(first,
		translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{
			{Type: translate.EscalationBlockText, Text: "intermediate"},
			{Type: translate.EscalationBlockToolCall, ID: "call", Name: "test"},
		}},
		translate.EscalationMessage{Role: translate.EscalationRoleTool, Blocks: []translate.EscalationBlock{
			{Type: translate.EscalationBlockToolResult, CallID: "call"},
		}},
	)
	var continuation router.ClassifierContext
	for attempt := range 3 {
		input, err := classifierContextForCall(loop)
		require.NoError(t, err)
		require.False(t, input.AtUserBoundary)
		require.Equal(t, initial.PrefixDigests, input.PrefixDigests[:len(initial.PrefixDigests)])
		require.NotEqual(t, initial.TurnDigest, input.TurnDigest)
		require.Equal(t, "first", input.CurrentUserMessage)
		require.Equal(t, router.ClassifierFeatures{UserMessageCount: 1, ToolCallCount: 1}, input.Features)
		require.Equal(t, 1, input.CompletedResponseCount)
		if attempt > 0 {
			require.Equal(t, continuation, input)
		}
		continuation = input
	}
	loop.Messages = append(loop.Messages, classifierTestText(translate.EscalationRoleUser, "next"))
	next, err := classifierContextForCall(loop)
	require.NoError(t, err)
	require.NotEqual(t, initial.TurnDigest, next.TurnDigest)
	require.Equal(t, initial.TurnDigest, next.PrecedingResponses[0].PrefixDigest)
	require.Equal(t, router.ClassifierFeatures{UserMessageCount: 2, ToolCallCount: 1}, next.Features)
}

func TestClassifierContextNoClippingAndBranchIdentity(t *testing.T) {
	longResponse := strings.Repeat("response ", 10000)
	prefix := classifierTestObservation(
		classifierTestText(translate.EscalationRoleUser, "first"),
		classifierTestText(translate.EscalationRoleAssistant, longResponse),
		classifierTestText(translate.EscalationRoleUser, "next"),
	)
	input, err := classifierContextForCall(prefix)
	require.NoError(t, err)
	require.Equal(t, longResponse, input.PrecedingResponses[0].Content)
	prefix.Messages[1].Blocks[0].Text += "changed"
	branch, err := classifierContextForCall(prefix)
	require.NoError(t, err)
	require.NotEqual(t, input.TurnDigest, branch.TurnDigest)
	require.Equal(t, input.PrecedingResponses[0].PrefixDigest, branch.PrecedingResponses[0].PrefixDigest)
}

func TestClassifierContextRejectsIncompleteAndAmbiguousHistory(t *testing.T) {
	user := classifierTestText(translate.EscalationRoleUser, "first")
	call := translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolCall, ID: "call", Name: "test"}}}
	toolResult := translate.EscalationMessage{Role: translate.EscalationRoleTool, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolResult, CallID: "call"}}}
	next := classifierTestText(translate.EscalationRoleUser, "next")
	fixtures := map[string]translate.EscalationObservation{
		"empty":                            classifierTestObservation(),
		"partial":                          {Messages: []translate.EscalationMessage{user}},
		"continuation":                     {HistoryComplete: true, ContinuationID: "prior", Messages: []translate.EscalationMessage{user}},
		"reference":                        {HistoryComplete: true, ItemReferenceIDs: []string{"item"}, Messages: []translate.EscalationMessage{user}},
		"missing user":                     classifierTestObservation(classifierTestText(translate.EscalationRoleAssistant, "answer")),
		"unknown tool":                     classifierTestObservation(user, toolResult),
		"duplicate call":                   classifierTestObservation(user, call, call),
		"duplicate result":                 classifierTestObservation(user, call, toolResult, toolResult),
		"open call at end":                 classifierTestObservation(user, call),
		"open call at user boundary":       classifierTestObservation(user, call, next),
		"late result across user boundary": classifierTestObservation(user, call, next, toolResult),
		"one of two calls unresolved": classifierTestObservation(user, call,
			translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolCall, ID: "other", Name: "test"}}}, toolResult),
		"user with no text blocks": classifierTestObservation(translate.EscalationMessage{Role: translate.EscalationRoleUser}),
		"mixed result and text": classifierTestObservation(user, call, translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{
			{Type: translate.EscalationBlockToolResult, CallID: "call"}, {Type: translate.EscalationBlockText, Text: "new user or reminder?"},
		}}),
	}
	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			_, err := classifierContextForCall(fixture)
			require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
		})
	}
}
