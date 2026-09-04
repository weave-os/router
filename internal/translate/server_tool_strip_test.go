package translate_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

// Anthropic executes web_search_*/web_fetch_* server-side; emitted to a non-Anthropic
// upstream they become phantom function tools (same failure claudecode_tool_filter.go prevents).
const anthropicServerToolBody = `{
	"model":"claude-opus-5",
	"messages":[{"role":"user","content":"what changed upstream?"}],
	"tools":[
		{"type":"web_search_20250305","name":"web_search","max_uses":5},
		{"type":"web_fetch_20250910","name":"web_fetch"},
		{"name":"Read","description":"","input_schema":{"type":"object"}}
	],
	"max_tokens":256
}`

// Only the native server tools are declared, so the whole tools array goes.
const anthropicServerToolOnlyBody = `{
	"model":"claude-opus-5",
	"messages":[{"role":"user","content":"Perform a web search for the query: weave router"}],
	"tools":[{"type":"web_search_20250305","name":"web_search"}],
	"tool_choice":{"type":"tool","name":"web_search"},
	"max_tokens":256
}`

func TestServerTools_AnthropicSourceOpenAITarget_Stripped(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(anthropicServerToolBody))
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "openai/gpt-5.6-luna"})
	require.NoError(t, err)

	names := emittedToolNames(t, out.Body)
	assert.ElementsMatch(t, []string{"Read"}, names,
		"client tools survive; Anthropic native server tools are dropped on Anthropic→OpenAI")
	assert.Equal(t, 2, out.Stats.ServerToolsStripped)
}

func TestServerTools_AnthropicSourceGeminiTarget_Stripped(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(anthropicServerToolBody))
	require.NoError(t, err)

	out, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-3.1-pro-preview"})
	require.NoError(t, err)

	names := emittedGeminiToolNames(t, out.Body)
	assert.ElementsMatch(t, []string{"Read"}, names)
	assert.Equal(t, 2, out.Stats.ServerToolsStripped)
}

// The Anthropic→Anthropic passthrough must keep them: that upstream is the one
// that can actually execute the search.
func TestServerTools_AnthropicSourceAnthropicTarget_Kept(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(anthropicServerToolBody))
	require.NoError(t, err)

	out, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5"})
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out.Body, &doc))
	raw, _ := doc["tools"].([]any)
	assert.Len(t, raw, 3, "Anthropic→Anthropic keeps the native server tools")
	assert.Equal(t, 0, out.Stats.ServerToolsStripped)
}

// A forced tool_choice naming a stripped tool would 400 on the upstream, so
// the strip drops the now-empty tools array and the dangling choice with it.
func TestServerTools_OnlyServerToolsDeclared_DropsToolChoice(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(anthropicServerToolOnlyBody))
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "openai/gpt-5.6-luna"})
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out.Body, &doc))
	assert.NotContains(t, doc, "tools")
	assert.NotContains(t, doc, "tool_choice")
	assert.Equal(t, 1, out.Stats.ServerToolsStripped)
}
