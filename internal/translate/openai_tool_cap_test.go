package translate_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/translate"
)

// Claude Code lists its built-ins first and MCP tools last. With tool search
// off and a large MCP surface the tool list exceeds the OpenAI cap; a positional
// cut dropped the MCP tools the session was actively calling, and the model
// then called them with guessed arguments.
const capMCPTool = "mcp__tracker__add_comment"
const capSearchedTool = "mcp__docs__get_file"

func overCapAnthropicBody(t *testing.T, messages []map[string]any, toolChoice map[string]any) []byte {
	t.Helper()
	var tools []map[string]any
	for i := 0; i < 200; i++ {
		tools = append(tools, map[string]any{
			"name":         fmt.Sprintf("mcp__bulk__tool_%03d", i),
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}},
		})
	}
	for _, name := range []string{"mcp__docs__search_files", capSearchedTool, capMCPTool} {
		tools = append(tools, map[string]any{
			"name":         name,
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"cloudId": map[string]any{"type": "string"}}, "required": []string{"cloudId"}},
		})
	}
	body := map[string]any{"model": "claude-opus-5-5", "max_tokens": 1024, "tools": tools, "messages": messages}
	if toolChoice != nil {
		body["tool_choice"] = toolChoice
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return raw
}

func emitOverCap(t *testing.T, body []byte) map[string][]string {
	t.Helper()
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	opts := translate.EmitOptions{TargetModel: "gpt-6-luna"}
	responses, err := env.PrepareOpenAIResponses(nil, opts)
	require.NoError(t, err)
	chat, err := env.PrepareOpenAI(nil, opts)
	require.NoError(t, err)
	declared := int(gjson.GetBytes(body, "tools.#").Int())
	assert.Equal(t, declared-128, responses.Stats.OpenAIToolsCapped, "responses capped count")
	assert.Equal(t, declared-128, chat.Stats.OpenAIToolsCapped, "chat capped count")
	return map[string][]string{
		"responses": emittedResponsesToolNames(t, responses.Body),
		"chat":      emittedToolNames(t, chat.Body),
	}
}

func TestOpenAIToolCapKeepsToolsUsedInHistory(t *testing.T) {
	body := overCapAnthropicBody(t, []map[string]any{
		{"role": "user", "content": "comment on the ticket"},
		{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "toolu_1", "name": capMCPTool, "input": map[string]any{"cloudId": "c1"}}}},
		{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok"}}},
	}, nil)
	for surface, names := range emitOverCap(t, body) {
		assert.Len(t, names, 128, surface)
		assert.Contains(t, names, capMCPTool, surface)
		assert.Equal(t, "mcp__bulk__tool_000", names[0], "%s: unreferenced tools keep request order", surface)
	}
}

func TestOpenAIToolCapKeepsSameServerTools(t *testing.T) {
	body := overCapAnthropicBody(t, []map[string]any{
		{"role": "user", "content": "read the doc"},
		{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "toolu_1", "name": "mcp__docs__search_files", "input": map[string]any{}}}},
		{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "toolu_1", "content": "ID: 1Abc"}}},
	}, nil)
	for surface, names := range emitOverCap(t, body) {
		assert.Contains(t, names, capSearchedTool, "%s: sibling on the called server", surface)
		assert.NotContains(t, names, capMCPTool, "%s: other servers keep request order", surface)
	}
}

func TestOpenAIToolCapKeepsToolSearchReferences(t *testing.T) {
	body := overCapAnthropicBody(t, []map[string]any{
		{"role": "user", "content": "open the doc"},
		{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "toolu_s", "name": "mcp__bulk__tool_000", "input": map[string]any{}}}},
		{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "toolu_s", "content": []map[string]any{{"type": "tool_reference", "tool_name": capSearchedTool}}}}},
	}, nil)
	for surface, names := range emitOverCap(t, body) {
		assert.Contains(t, names, capSearchedTool, surface)
	}
}

func TestOpenAIToolCapKeepsForcedToolChoice(t *testing.T) {
	body := overCapAnthropicBody(t, []map[string]any{{"role": "user", "content": "comment"}},
		map[string]any{"type": "tool", "name": capMCPTool})
	for surface, names := range emitOverCap(t, body) {
		assert.Contains(t, names, capMCPTool, surface)
	}
}

func TestOpenAIToolCapLeavesSmallToolListsUntouched(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"b","input_schema":{"type":"object"}},{"name":"a","input_schema":{"type":"object"}}]}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	out, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{TargetModel: "gpt-6-luna"})
	require.NoError(t, err)
	assert.Equal(t, []string{"b", "a"}, emittedResponsesToolNames(t, out.Body))
	assert.Zero(t, out.Stats.OpenAIToolsCapped)
}

func TestOpenAIChatToResponsesToolCapKeepsCalledTools(t *testing.T) {
	var tools []map[string]any
	for i := 0; i < 200; i++ {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": fmt.Sprintf("bulk_%03d", i), "parameters": map[string]any{"type": "object"}}})
	}
	tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": "add_comment", "parameters": map[string]any{"type": "object"}}})
	raw, err := json.Marshal(map[string]any{
		"model": "gpt-6-luna",
		"tools": tools,
		"messages": []map[string]any{
			{"role": "user", "content": "comment"},
			{"role": "assistant", "tool_calls": []map[string]any{{"id": "call_1", "type": "function", "function": map[string]any{"name": "add_comment", "arguments": "{}"}}}},
			{"role": "tool", "tool_call_id": "call_1", "content": "ok"},
		},
	})
	require.NoError(t, err)
	env, err := translate.ParseOpenAI(raw)
	require.NoError(t, err)
	out, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{TargetModel: "gpt-6-luna"})
	require.NoError(t, err)
	names := emittedResponsesToolNames(t, out.Body)
	assert.Len(t, names, 128)
	assert.Contains(t, names, "add_comment")
}
