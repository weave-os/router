package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func anthropicEnv(t *testing.T, body string) *RequestEnvelope {
	t.Helper()
	env, err := ParseAnthropic([]byte(body))
	require.NoError(t, err)
	return env
}

func TestSearchToolUseRecency_AdvertisedOnlyIsNotUse(t *testing.T) {
	env := anthropicEnv(t, `{
		"model":"claude-sonnet-4-5",
		"tools":[{"type":"web_search_20250305","name":"web_search"}],
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[{"type":"text","text":"hi"}]},
			{"role":"user","content":"write a function"}
		]
	}`)
	assert.Equal(t, -1, env.SearchToolUseRecency())
}

func TestSearchToolUseRecency_ServerToolUseCurrentTurn(t *testing.T) {
	env := anthropicEnv(t, `{
		"messages":[
			{"role":"user","content":"look this up"},
			{"role":"assistant","content":[
				{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"weather"}}
			]},
			{"role":"user","content":[{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]}]}
		]
	}`)
	assert.Equal(t, 0, env.SearchToolUseRecency())
}

func TestSearchToolUseRecency_CountsAssistantTurnsSinceUse(t *testing.T) {
	env := anthropicEnv(t, `{
		"messages":[
			{"role":"assistant","content":[
				{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"weather"}}
			]},
			{"role":"user","content":[{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]}]},
			{"role":"assistant","content":[{"type":"text","text":"it is sunny"}]},
			{"role":"user","content":"now refactor"},
			{"role":"assistant","content":[{"type":"text","text":"done"}]},
			{"role":"user","content":"thanks"}
		]
	}`)
	assert.Equal(t, 2, env.SearchToolUseRecency())
}

func TestSearchToolUseRecency_ClaudeCodeClientTool(t *testing.T) {
	env := anthropicEnv(t, `{
		"messages":[
			{"role":"user","content":"search the docs"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_1","name":"WebSearch","input":{"query":"gjson docs"}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"results"}]}
		]
	}`)
	assert.Equal(t, 0, env.SearchToolUseRecency())
}

func TestSearchToolUseRecency_ClaudeCodeSearchSubTurn(t *testing.T) {
	env := anthropicEnv(t, `{
		"tools":[{"type":"web_search_20250305","name":"web_search"}],
		"messages":[
			{"role":"user","content":"Perform a web search for the query: golang lru"}
		]
	}`)
	assert.Equal(t, 0, env.SearchToolUseRecency())
}

func TestSearchToolUseRecency_OpenAIResponsesWebSearchCall(t *testing.T) {
	env, err := ParseOpenAI([]byte(`{
		"model":"gpt-5",
		"input":[
			{"type":"message","role":"user","content":"look this up"},
			{"type":"web_search_call","id":"ws_1","status":"completed"},
			{"type":"message","role":"assistant","content":"found it"},
			{"type":"message","role":"user","content":"now refactor"}
		]
	}`))
	require.NoError(t, err)
	assert.Equal(t, 1, env.SearchToolUseRecency())
}

func TestSearchToolUseRecency_GeminiUnsupported(t *testing.T) {
	env, err := ParseGemini([]byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	require.NoError(t, err)
	assert.Equal(t, -1, env.SearchToolUseRecency())
}
