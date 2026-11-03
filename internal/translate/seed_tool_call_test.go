package translate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/usage"
)

var seedDeclared = map[string]struct{}{"Shell": {}, "Read": {}, "Bash": {}}

func TestParseSeedToolCalls(t *testing.T) {
	cases := map[string]struct {
		text  string
		ok    bool
		names []string
	}{
		"malformed closer":   {`<seed:tool_call><function_name>Shell</function_name><argument>{"command": "echo hello"}</argument></seed>tool_call>`, true, []string{"Shell"}},
		"well-formed closer": {"  <seed:tool_call>\n<function_name> Read </function_name>\n<argument>{\"path\":\"a\"}</argument>\n</seed:tool_call>\n", true, []string{"Read"}},
		"two calls":          {`<seed:tool_call><function_name>Read</function_name><argument>{}</argument></seed:tool_call> <seed:tool_call><function_name>Shell</function_name><argument>{"command":"ls"}</argument></seed>tool_call>`, true, []string{"Read", "Shell"}},
		"no argument":        {`<seed:tool_call><function_name>Read</function_name></seed:tool_call>`, true, []string{"Read"}},
		"undeclared":         {`<seed:tool_call><function_name>Delete</function_name><argument>{}</argument></seed:tool_call>`, false, nil},
		"invalid json":       {`<seed:tool_call><function_name>Shell</function_name><argument>{"command": </argument></seed:tool_call>`, false, nil},
		"array argument":     {`<seed:tool_call><function_name>Shell</function_name><argument>["ls"]</argument></seed:tool_call>`, false, nil},
		"unclosed":           {`<seed:tool_call><function_name>Shell</function_name><argument>{}</argument>`, false, nil},
		"trailing prose":     {`<seed:tool_call><function_name>Shell</function_name><argument>{}</argument></seed:tool_call> done`, false, nil},
		"leading prose":      {`ok <seed:tool_call><function_name>Shell</function_name><argument>{}</argument></seed:tool_call>`, false, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			calls, ok := parseSeedToolCalls(tc.text, seedDeclared)
			require.Equal(t, tc.ok, ok)
			var names []string
			for _, call := range calls {
				names = append(names, call.Name)
				assert.True(t, gjson.Valid(call.Arguments))
			}
			assert.Equal(t, tc.names, names)
		})
	}
}

func TestSeedToolCallGate_ReleasesProseImmediately(t *testing.T) {
	var gate seedToolCallGate
	assert.Equal(t, "Hello", gate.Feed("Hello"))
	assert.Equal(t, " <seed:tool_call>", gate.Feed(" <seed:tool_call>"), "a mid-prose tag stays text")

	var partial seedToolCallGate
	assert.Equal(t, "", partial.Feed("\n<se"))
	assert.Equal(t, "\n<sea shell", partial.Feed("a shell"), "held bytes release once the prefix diverges")
}

func driveSeedAnthropic(t *testing.T, thinkTags bool, events ...string) (string, ResponseSummary) {
	t.Helper()
	rec := httptest.NewRecorder()
	translator := NewAnthropicSSETranslator(rec, "xiaomi/mimo-v2.5-pro", nil).
		WithRequestHadTools(true).
		WithThinkTagReasoning(thinkTags).
		WithSeedToolCallMarkup(seedDeclared)
	translator.Header().Set("Content-Type", "text/event-stream")
	translator.WriteHeader(http.StatusOK)
	for _, event := range events {
		_, err := translator.Write([]byte(event))
		require.NoError(t, err)
	}
	require.NoError(t, translator.Finalize())
	return rec.Body.String(), translator.Summary()
}

func seedChunk(content string) string {
	return `data: {"id":"c1","choices":[{"index":0,"delta":{"content":` + string(jsonStringLiteralNoHTMLEscape(content)) + `},"finish_reason":null}]}` + "\n\n"
}

func jsonStringLiteralNoHTMLEscape(s string) []byte {
	var b strings.Builder
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(s)
	return []byte(strings.TrimSpace(b.String()))
}

const seedStopChunk = `data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"

func TestAnthropicSSETranslator_SeedToolCallAfterThinkTag(t *testing.T) {
	body, summary := driveSeedAnthropic(t, true,
		seedChunk("<think>need a shell</think>\n\n<seed:tool"),
		seedChunk(`_call><function_name>Bash</function_name><argument>{"command": "ls"}</arg`),
		seedChunk("ument></seed:tool_call>"),
		seedStopChunk,
	)
	assert.NotContains(t, body, "seed:tool_call")
	assert.Contains(t, body, `"type":"thinking"`)
	assert.Contains(t, body, `"name":"Bash"`)
	assert.Contains(t, body, `"stop_reason":"tool_use"`)
	assert.Equal(t, 1, summary.SeedToolCallsRecovered)
	assert.Equal(t, 1, summary.ToolUseBlocks)
	assert.False(t, summary.TextOnlyTurnNudged)
}

func TestAnthropicSSETranslator_SeedToolCallUnparseableFallsBackToNudge(t *testing.T) {
	markup := `<seed:tool_call><function_name>Bash</function_name><argument>{"command": </argument></seed:tool_call>`
	body, summary := driveSeedAnthropic(t, false, seedChunk(markup), seedStopChunk)
	assert.Contains(t, body, strings.ReplaceAll(markup, `"`, `\"`), "unparseable markup is forwarded as text")
	assert.Zero(t, summary.SeedToolCallsRecovered)
	assert.True(t, summary.TextOnlyTurnNudged, "the leaked markup still gets the recovery nudge")
}

func TestOpenAIToAnthropicResponse_SeedToolCallMarkup(t *testing.T) {
	body := []byte(`{"id":"c1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"<seed:tool_call><function_name>Bash</function_name><argument>{\"command\": \"echo hello\"}</argument></seed>tool_call>"},"finish_reason":"stop"}]}`)
	out, _, err := openAIToAnthropicResponse(body, "m", nil, false, false, seedDeclared, usage.Facts{}, 1)
	require.NoError(t, err)
	assert.Equal(t, "tool_use", gjson.GetBytes(out, "stop_reason").String())
	require.Len(t, gjson.GetBytes(out, "content").Array(), 1)
	assert.Equal(t, "Bash", gjson.GetBytes(out, "content.0.name").String())
	assert.Equal(t, "echo hello", gjson.GetBytes(out, "content.0.input.command").String())

	untouched, _, err := openAIToAnthropicResponse(body, "m", nil, false, false, nil, usage.Facts{}, 1)
	require.NoError(t, err)
	assert.Equal(t, "end_turn", gjson.GetBytes(untouched, "stop_reason").String())
	assert.Contains(t, gjson.GetBytes(untouched, "content.0.text").String(), "<seed:tool_call>")
}

// A stream cut before any terminal releases the held markup unchanged.
func TestSeedToolCallChatWriter_ReleasesHeldTextWithoutTerminal(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	w := NewSeedToolCallChatWriter(rec, seedDeclared)
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"<seed:tool_call><function_name>Shell"},"finish_reason":null}]}` + "\n\n"))
	require.NoError(t, err)
	assert.NotContains(t, rec.Body.String(), "seed:tool_call", "candidate markup is held while it streams")
	require.NoError(t, w.Finalize())
	assert.Contains(t, rec.Body.String(), `"content":"<seed:tool_call><function_name>Shell"`)
	assert.Zero(t, w.ConvertedToolCalls())
}
