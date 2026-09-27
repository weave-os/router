package translate_test

import (
	"net/http"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readPrefixBody is a Claude Code turn: a Read of tab-indented Go (separator
// tab followed by indentation tabs), a Bash result that merely looks numbered,
// and an Edit tool whose description names the prefix format.
const readPrefixBody = `{"model":"claude-opus-4-8","max_tokens":1024,
"tools":[
 {"name":"Edit","description":"Strip the Read line prefix (line number + tab) before matching.","input_schema":{"type":"object"}},
 {"name":"Read","description":"Reads a file.","input_schema":{"type":"object"}}],
"messages":[
 {"role":"user","content":"fix it"},
 {"role":"assistant","content":[
  {"type":"tool_use","id":"toolu_read","name":"Read","input":{"file_path":"a.go"}},
  {"type":"tool_use","id":"toolu_bash","name":"Bash","input":{"command":"wc -l a.go"}}]},
 {"role":"user","content":[
  {"type":"tool_result","tool_use_id":"toolu_read","content":"1\tpackage a\n2\t\n3\tfunc f() {\n4\t\treturn\n5\t}"},
  {"type":"tool_result","tool_use_id":"toolu_bash","content":"12\ta.go"}]}]}`

const rewrittenRead = "1→package a\n2→\n3→func f() {\n4→\treturn\n5→}"

// decodedJSON undoes the emitters' two escape spellings of newline and tab so
// assertions read as file content.
func decodedJSON(body []byte) string {
	return strings.NewReplacer(`\u000a`, "\n", `\u0009`, "\t", `\n`, "\n", `\t`, "\t").Replace(string(body))
}

func TestReadPrefixes_NonAnthropicTargetsGetUnambiguousSeparator(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(readPrefixBody))
	require.NoError(t, err)

	for name, prepare := range map[string]func() ([]byte, error){
		"responses": func() ([]byte, error) {
			p, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{TargetModel: "gpt-5.6-luna"})
			return p.Body, err
		},
		"chat": func() ([]byte, error) {
			p, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{TargetModel: "grok-4.6"})
			return p.Body, err
		},
		"gemini": func() ([]byte, error) {
			p, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-2.5-pro"})
			return p.Body, err
		},
	} {
		body, err := prepare()
		require.NoError(t, err, name)
		out := decodedJSON(body)
		assert.Contains(t, out, rewrittenRead, "%s: separator tab replaced, indentation tab kept", name)
		assert.NotContains(t, out, "4\t\treturn", name)
		assert.Contains(t, out, "12\ta.go", "%s: only Read results are rewritten", name)
		assert.Contains(t, out, "line number + →", "%s: Edit's instructions match the rewritten prefix", name)
	}
}

func TestReadPrefixes_AnthropicTargetKeepsClaudeCodeFormat(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(readPrefixBody))
	require.NoError(t, err)
	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "claude-opus-4-8"})
	require.NoError(t, err)
	out := decodedJSON(prep.Body)
	assert.Contains(t, out, "4\t\treturn")
	assert.NotContains(t, out, "→")
}

func TestReadPrefixes_ArrayContentAndStableOutput(t *testing.T) {
	body := `{"model":"claude-opus-4-8","max_tokens":64,"messages":[
 {"role":"user","content":"read"},
 {"role":"assistant","content":[{"type":"tool_use","id":"r1","name":"Read","input":{}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"r1","content":[{"type":"text","text":"7\t\tx := 1"}]}]}]}`
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	first, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{TargetModel: "grok-4.6"})
	require.NoError(t, err)
	assert.Contains(t, decodedJSON(first.Body), "7→\tx := 1")
	second, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{TargetModel: "grok-4.6"})
	require.NoError(t, err)
	assert.Equal(t, string(first.Body), string(second.Body), "a stable rewrite keeps the prompt cache warm")
}
