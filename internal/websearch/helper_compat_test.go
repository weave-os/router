package websearch_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/websearch"
)

const currentSearchHelperPrompt = "You run a web search for another Claude model, which is waiting on the result and can search again. Search once, then reply with a brief summary of the results."

func TestClaudeCodeSearchHelperPromptCompatibility(t *testing.T) {
	for _, prompt := range []string{"You are an assistant for performing a web search tool use", currentSearchHelperPrompt} {
		for _, system := range []any{prompt, []any{map[string]any{"type": "text", "text": prompt}}} {
			body, err := json.Marshal(map[string]any{
				"system":   system,
				"tools":    []any{map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": 8}},
				"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Perform a web search for the query: official Go documentation"}}}},
			})
			require.NoError(t, err)
			require.True(t, websearch.IsClaudeCodeWebSearchHelper(body), "known helper prompt must be recognized: %s", prompt)
			converted, err := websearch.ResponsesRequest(body, "selected")
			require.NoError(t, err)
			assert.Contains(t, string(converted), "official Go documentation")
		}
	}
}

func TestClaudeCodeSearchHelperRejectsUnknownSystemPrompts(t *testing.T) {
	body := []byte(`{"system":"You are a general coding assistant","tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"Perform a web search for the query: official Go documentation"}]}`)
	assert.False(t, websearch.IsClaudeCodeWebSearchHelper(body))
}
