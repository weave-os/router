package proxy

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

func TestOpenCodeCompletionUsesRoutingViewForEachWireFormat(t *testing.T) {
	const notification = "<task id=\"ses_123abc\" state=\"completed\">\n<task_result>\nChild inspection complete\n</task_result>\n</task>"
	const running = "<task id=\"ses_123abc\" state=\"running\">\n<task_result>\nTask started\n</task_result>\n</task>"
	for _, test := range []struct {
		name  string
		body  string
		parse func([]byte) (*translate.RequestEnvelope, error)
	}{
		{name: "Anthropic", body: `{"messages":[{"role":"user","content":"Fix the test"},{"role":"assistant","content":[{"type":"tool_use","id":"task_start","name":"task","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"task_start","content":` + strconv.Quote(running) + `}]},{"role":"assistant","content":"Waiting for the background result"},{"role":"user","content":` + strconv.Quote(notification) + `}]}`, parse: translate.ParseAnthropic},
		{name: "OpenAI chat", body: `{"messages":[{"role":"user","content":"Fix the test"},{"role":"assistant","tool_calls":[{"type":"function","id":"task_start","function":{"name":"task","arguments":"{}"}}]},{"role":"tool","tool_call_id":"task_start","content":` + strconv.Quote(running) + `},{"role":"assistant","content":"Waiting for the background result"},{"role":"user","content":` + strconv.Quote(notification) + `}]}`, parse: translate.ParseOpenAI},
		{name: "Gemini", body: `{"contents":[{"role":"user","parts":[{"text":"Fix the test"}]},{"role":"model","parts":[{"functionCall":{"name":"task","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"task","response":{"output":` + strconv.Quote(running) + `}}}]},{"role":"model","parts":[{"text":"Waiting for the background result"}]},{"role":"user","parts":[{"text":` + strconv.Quote(notification) + `}]}]}`, parse: translate.ParseGemini},
	} {
		t.Run(test.name, func(t *testing.T) {
			env, err := test.parse([]byte(test.body))
			require.NoError(t, err)
			ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppOpencode})
			prompt, messages := routingInputForRequest(ctx, env, "unfiltered", true)
			require.Len(t, messages, 5)
			assert.Equal(t, "Fix the test", prompt)
			assert.Equal(t, string(translate.EscalationRoleAssistant), messages[4].Role)
			assert.Equal(t, notification, messages[4].Text)
			assert.False(t, routingEndsWithUserPrompt(ctx, env))
			assert.Equal(t, string(translate.EscalationRoleUser), env.ConversationMessages()[4].Role, "the provider envelope must stay intact")
			assert.True(t, routingEndsWithUserPrompt(context.Background(), env), "generic clients retain ordinary user-message semantics")
		})
	}
}

func TestOpenCodeCompletionBehindSystemNoticeIsNotAUserPrompt(t *testing.T) {
	const body = `{"messages":[
		{"role":"user","content":"Fix the test"},
		{"role":"assistant","tool_calls":[{"type":"function","id":"task_start","function":{"name":"task","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"task_start","content":"<task id=\"ses_123abc\" state=\"running\">\n<task_result>\nTask started\n</task_result>\n</task>"},
		{"role":"assistant","content":"Waiting for the background result"},
		{"role":"user","content":"<task id=\"ses_123abc\" state=\"completed\">\n<task_result>\nChild inspection complete\n</task_result>\n</task>"},
		{"role":"system","content":"A tool is available."}
	]}`
	env, err := translate.ParseOpenAI([]byte(body))
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppOpencode})
	assert.False(t, routingEndsWithUserPrompt(ctx, env))
	assert.True(t, routingEndsWithUserPrompt(context.Background(), env))
}

func TestOpenCodeCompletedTaskReceiptDoesNotConsumeLaterUserQuote(t *testing.T) {
	const body = `{"messages":[
		{"role":"user","content":"Fix the test"},
		{"role":"assistant","tool_calls":[{"type":"function","id":"task_start","function":{"name":"task","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"task_start","content":"<task id=\"ses_123abc\" state=\"running\">\n<task_result>\nTask started\n</task_result>\n</task>"},
		{"role":"assistant","content":"Waiting for the background result"},
		{"role":"user","content":"<task id=\"ses_123abc\" state=\"completed\">\n<task_result>\nChild inspection complete\n</task_result>\n</task>"},
		{"role":"assistant","content":"The child finished."},
		{"role":"user","content":"<task id=\"ses_123abc\" state=\"completed\">\n<task_result>\nChild inspection complete\n</task_result>\n</task>"}
	]}`
	env, err := translate.ParseOpenAI([]byte(body))
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppOpencode})
	_, messages := routingInputForRequest(ctx, env, "unfiltered", true)
	require.Len(t, messages, 7)
	assert.Equal(t, string(translate.EscalationRoleAssistant), messages[4].Role)
	assert.Equal(t, string(translate.EscalationRoleUser), messages[6].Role)
	assert.True(t, routingEndsWithUserPrompt(ctx, env))
}

func TestClaudeNotificationProjectionDoesNotRewriteGenericUserInput(t *testing.T) {
	const notification = "<task-notification><task-id>1</task-id></task-notification>"
	const body = `{"messages":[{"role":"user","content":"Fix the test"},{"role":"assistant","content":"Working"},{"role":"user","content":"` + notification + `"}]}`
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	_, genericMessages := routingInputForRequest(context.Background(), env, "unfiltered", true)
	require.Len(t, genericMessages, 3)
	assert.Equal(t, notification, genericMessages[2].Text)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppClaudeCode})
	prompt, claudeMessages := routingInputForRequest(ctx, env, "unfiltered", true)
	require.Len(t, claudeMessages, 2)
	assert.Equal(t, "Fix the test", prompt)
}

func TestConversationMessagesForRoutingConvertsAtProxyBoundary(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-7",
		"messages":[
			{
				"role":"assistant",
				"content":[{"type":"tool_use","name":"Read","input":{"file_path":"README.md"}}]
			},
			{
				"role":"user",
				"content":[{"type":"tool_result","tool_use_id":"toolu_123","is_error":true,"content":"raw output"}]
			}
		]
	}`))
	require.NoError(t, err)

	messages := conversationMessagesForRouting(env)
	require.Len(t, messages, 2)
	assert.Equal(t, "assistant", messages[0].Role)
	require.Len(t, messages[0].ToolCalls, 1)
	assert.Equal(t, "Read", messages[0].ToolCalls[0].Name)
	assert.Equal(t, []string{"file_path"}, messages[0].ToolCalls[0].InputKeys)
	assert.Equal(t, `{"file_path":"README.md"}`, messages[0].ToolCalls[0].InputJSON)
	assert.Equal(t, "user", messages[1].Role)
	assert.Empty(t, messages[1].Text)
	require.Len(t, messages[1].ToolResults, 1)
	assert.Equal(t, "toolu_123", messages[1].ToolResults[0].ToolUseID)
	assert.True(t, messages[1].ToolResults[0].IsError)
	assert.Equal(t, "raw output", messages[1].ToolResults[0].Text)
}
