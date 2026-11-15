package translate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

func TestConversationMessagesStripsClaudeInjectedBlocks(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-7",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"<system-reminder>internal reminder</system-reminder>"},
				{"type":"text","text":"<command-name>do-not-route-on-this</command-name>"},
				{"type":"text","text":"can you help me brainstorm a bit"}
			]
		}]
	}`))
	require.NoError(t, err)

	messages := env.ConversationMessages()
	require.Len(t, messages, 1)
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, "can you help me brainstorm a bit", messages[0].Text)
	clientMessages := env.ConversationMessagesWithClientText()
	require.Len(t, clientMessages, 1)
	assert.Equal(t, "can you help me brainstorm a bit", clientMessages[0].Text)
}

func TestConversationMessagesPreservesClaudeTextAfterInjectedTaskNotice(t *testing.T) {
	for _, test := range []struct {
		name    string
		content any
	}{
		{
			name:    "string content",
			content: "<task-notification><task-id>1</task-id></task-notification>\nPlease inspect the follow-up failure",
		},
		{
			name:    "text block",
			content: []any{map[string]any{"type": "text", "text": "<task-notification><task-id>1</task-id></task-notification>\nPlease inspect the follow-up failure"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic(mustMarshalJSON(t, map[string]any{
				"model":      "claude-sonnet-4-6",
				"max_tokens": 256,
				"messages": []any{
					map[string]any{"role": "user", "content": "Implement the requested fix"},
					map[string]any{"role": "assistant", "content": "I will inspect and update the code."},
					map[string]any{"role": "user", "content": test.content},
				},
			}))
			require.NoError(t, err)
			messages, changed := env.ClaudeCodeRoutingMessages()
			assert.True(t, changed)
			require.Len(t, messages, 3)
			assert.Equal(t, "Please inspect the follow-up failure", messages[2].Text)
			assert.True(t, env.EndsWithUserPrompt())
		})
	}
}

func TestConversationMessagesDropsScalarClaudeTaskNotificationOnly(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-sonnet-4-6",
		"max_tokens":256,
		"messages":[
			{"role":"user","content":"Implement the requested fix"},
			{"role":"assistant","content":"I will inspect and update the code."},
			{"role":"user","content":"<task-notification><task-id>1</task-id></task-notification>"}
		]
	}`))
	require.NoError(t, err)
	messages, changed := env.ClaudeCodeRoutingMessages()
	assert.True(t, changed)
	require.Len(t, messages, 2)
	assert.Equal(t, "Implement the requested fix", messages[0].Text)
	assert.False(t, env.EndsWithUserPrompt())
}

func TestConversationMessagesPreservesUnrecognizedTaggedUserText(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-sonnet-4-6",
		"max_tokens":256,
		"messages":[{"role":"user","content":"<result>this is text the person wrote</result>"}]
	}`))
	require.NoError(t, err)
	messages := env.ConversationMessages()
	require.Len(t, messages, 1)
	assert.Equal(t, "<result>this is text the person wrote</result>", messages[0].Text)
}

func TestSubagentToolResultsDoNotReplaceTheUserTask(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		parse func([]byte) (*translate.RequestEnvelope, error)
	}{
		{
			name: "Anthropic task or Pi dispatch result",
			body: `{"messages":[
				{"role":"user","content":"Investigate and fix the failing test"},
				{"role":"assistant","content":[{"type":"tool_use","id":"child","name":"Task","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"child","content":"Child inspection complete"}]}
			]}`,
			parse: translate.ParseAnthropic,
		},
		{
			name: "Chat task tool result",
			body: `{"messages":[
				{"role":"user","content":"Investigate and fix the failing test"},
				{"role":"assistant","tool_calls":[{"id":"child","type":"function","function":{"name":"task","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"child","content":"Child inspection complete"}
			]}`,
			parse: translate.ParseOpenAI,
		},
		{
			name: "Gemini function response",
			body: `{"contents":[
				{"role":"user","parts":[{"text":"Investigate and fix the failing test"}]},
				{"role":"model","parts":[{"functionCall":{"name":"task","args":{}}}]},
				{"role":"user","parts":[{"functionResponse":{"name":"task","response":{"output":"Child inspection complete"}}}]}
			]}`,
			parse: translate.ParseGemini,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			env, err := test.parse([]byte(test.body))
			require.NoError(t, err)
			messages := env.ConversationMessages()
			require.Len(t, messages, 3)
			var latestTask string
			for _, message := range messages {
				if message.Role == string(translate.EscalationRoleUser) && message.Text != "" {
					latestTask = message.Text
				}
			}
			assert.Equal(t, "Investigate and fix the failing test", latestTask)
			assert.Empty(t, messages[2].Text)
			require.Len(t, messages[2].ToolResults, 1)
			assert.Contains(t, messages[2].ToolResults[0].Text, "Child inspection complete")
			assert.False(t, env.EndsWithUserPrompt())
		})
	}
}

func TestConversationMessagesGeminiMissingRoleDefaultsToUser(t *testing.T) {
	env, err := translate.ParseGemini([]byte(`{
		"contents":[
			{"parts":[{"text":"can you help me brainstorm a bit"}]},
			{"role":"model","parts":[{"text":"sure"}]}
		]
	}`))
	require.NoError(t, err)

	messages := env.ConversationMessages()
	require.Len(t, messages, 2)
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, "can you help me brainstorm a bit", messages[0].Text)
	assert.Equal(t, "assistant", messages[1].Role)
}

func TestConversationMessagesPreservesAnthropicToolResultMarker(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-7",
		"messages":[{
			"role":"user",
			"content":[{
				"type":"tool_result",
				"tool_use_id":"toolu_123",
				"is_error":true,
				"content":"large raw tool output"
			}]
		}]
	}`))
	require.NoError(t, err)

	messages := env.ConversationMessages()
	require.Len(t, messages, 1)
	assert.Equal(t, "user", messages[0].Role)
	assert.Empty(t, messages[0].Text)
	require.Len(t, messages[0].ToolResults, 1)
	assert.Equal(t, "toolu_123", messages[0].ToolResults[0].ToolUseID)
	assert.True(t, messages[0].ToolResults[0].IsError)
	assert.Equal(t, "large raw tool output", messages[0].ToolResults[0].Text)
}

func TestConversationMessagesPreservesOpenAIToolResultMarker(t *testing.T) {
	env, err := translate.ParseOpenAI([]byte(`{
		"model":"gpt-4o",
		"messages":[
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_123","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"README.md\"}"}}]},
			{"role":"tool","tool_call_id":"call_123","content":"large raw tool output"}
		]
	}`))
	require.NoError(t, err)

	messages := env.ConversationMessages()
	require.Len(t, messages, 2)
	assert.Equal(t, "assistant", messages[0].Role)
	require.Len(t, messages[0].ToolCalls, 1)
	assert.Equal(t, "Read", messages[0].ToolCalls[0].Name)
	assert.Equal(t, `{"file_path":"README.md"}`, messages[0].ToolCalls[0].InputJSON)
	assert.Equal(t, "user", messages[1].Role)
	assert.Empty(t, messages[1].Text)
	require.Len(t, messages[1].ToolResults, 1)
	assert.Equal(t, "call_123", messages[1].ToolResults[0].ToolUseID)
	assert.Equal(t, "large raw tool output", messages[1].ToolResults[0].Text)
}

func TestConversationMessagesPreservesGeminiToolResultMarker(t *testing.T) {
	env, err := translate.ParseGemini([]byte(`{
		"contents":[
			{"role":"model","parts":[{"functionCall":{"name":"Bash","args":{"command":"pwd"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"Bash","response":{"output":"large raw tool output"}}}]}
		]
	}`))
	require.NoError(t, err)

	messages := env.ConversationMessages()
	require.Len(t, messages, 2)
	assert.Equal(t, "assistant", messages[0].Role)
	require.Len(t, messages[0].ToolCalls, 1)
	assert.Equal(t, "Bash", messages[0].ToolCalls[0].Name)
	assert.Equal(t, "user", messages[1].Role)
	assert.Empty(t, messages[1].Text)
	require.Len(t, messages[1].ToolResults, 1)
	assert.Equal(t, "Bash", messages[1].ToolResults[0].ToolUseID)
	assert.Equal(t, "large raw tool output", messages[1].ToolResults[0].Text)
}

func TestConversationMessagesDropsNamelessOpenAIToolCalls(t *testing.T) {
	env, err := translate.ParseOpenAI([]byte(`{
		"model":"gpt-4o",
		"messages":[{
			"role":"assistant",
			"content":null,
			"tool_calls":[
				{"id":"call_empty","type":"function","function":{"name":"","arguments":"{\"path\":\"a\"}"}},
				{"id":"call_read","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"README.md\"}"}}
			]
		}]
	}`))
	require.NoError(t, err)

	messages := env.ConversationMessages()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].ToolCalls, 1)
	assert.Equal(t, "Read", messages[0].ToolCalls[0].Name)
}

func TestConversationMessagesDropsNamelessGeminiToolCalls(t *testing.T) {
	env, err := translate.ParseGemini([]byte(`{
		"contents":[{
			"role":"model",
			"parts":[
				{"functionCall":{"name":"","args":{"path":"a"}}},
				{"functionCall":{"name":"Read","args":{"file_path":"README.md"}}}
			]
		}]
	}`))
	require.NoError(t, err)

	messages := env.ConversationMessages()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].ToolCalls, 1)
	assert.Equal(t, "Read", messages[0].ToolCalls[0].Name)
}

func TestAvailableToolNamesProviderNeutral(t *testing.T) {
	anthropicEnv, err := translate.ParseAnthropic([]byte(`{
		"model":"claude-opus-4-7",
		"tools":[
			{"name":"Read","input_schema":{"type":"object"}},
			{"name":"Grep","input_schema":{"type":"object"}},
			{"name":"Read","input_schema":{"type":"object"}}
		],
		"messages":[{"role":"user","content":"inspect"}]
	}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"Grep", "Read"}, anthropicEnv.AvailableToolNames())

	openAIEnv, err := translate.ParseOpenAI([]byte(`{
		"model":"gpt-4o",
		"tools":[
			{"type":"function","function":{"name":"Bash","parameters":{"type":"object"}}},
			{"type":"function","function":{"name":"","parameters":{"type":"object"}}}
		],
		"messages":[{"role":"user","content":"inspect"}]
	}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"Bash"}, openAIEnv.AvailableToolNames())
}
