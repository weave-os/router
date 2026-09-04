package websearch_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/websearch"
)

func TestFindServerToolAcceptsEveryDatedVersion(t *testing.T) {
	for _, toolType := range []string{"web_search_20250305", "web_search_20260209"} {
		body := []byte(`{"tools":[{"type":"` + toolType + `","name":"web_search","max_uses":5}]}`)
		tool, ok := websearch.FindServerTool(body)
		if !ok {
			t.Fatalf("%s: not detected as a native server tool", toolType)
		}
		if tool.Type != toolType || tool.Name != "web_search" {
			t.Fatalf("%s: got %+v", toolType, tool)
		}
	}
}

func TestFindServerToolIgnoresClientFunctionTools(t *testing.T) {
	body := []byte(`{"tools":[{"name":"web_search","description":"search","input_schema":{"type":"object"}}]}`)
	if _, ok := websearch.FindServerTool(body); ok {
		t.Fatal("a client function tool named web_search must not be treated as a server tool")
	}
}

func TestStripServerToolsKeepsClientTools(t *testing.T) {
	body := []byte(`{"tools":[{"name":"Bash"},{"type":"web_search_20250305","name":"web_search"},{"name":"Read"}],"tool_choice":{"type":"auto"}}`)
	out, removed := websearch.StripServerTools(body)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	names := gjson.GetBytes(out, "tools.#.name").Array()
	if len(names) != 2 || names[0].String() != "Bash" || names[1].String() != "Read" {
		t.Fatalf("remaining tools = %v", names)
	}
	if !gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatal("tool_choice dropped while client tools remain")
	}
}

func TestStripServerToolsDropsForcedChoiceForRemovedTool(t *testing.T) {
	body := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"Read"}],"tool_choice":{"type":"tool","name":"web_search"}}`)
	out, removed := websearch.StripServerTools(body)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if names := gjson.GetBytes(out, "tools.#.name").Array(); len(names) != 1 || names[0].String() != "Read" {
		t.Fatalf("remaining tools = %v", names)
	}
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatal("tool_choice naming the stripped tool was not dropped")
	}
}

func TestStripServerToolsDropsToolChoiceWhenNothingIsLeft(t *testing.T) {
	body := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"tool_choice":{"type":"tool","name":"web_search"}}`)
	out, removed := websearch.StripServerTools(body)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if gjson.GetBytes(out, "tools").Exists() || gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("empty tools/tool_choice left behind: %s", out)
	}
}

func TestDetectSearchTurnClaudeCodeSubTurn(t *testing.T) {
	body := []byte(`{
		"model":"claude-sonnet-5",
		"tools":[{"type":"web_search_20250305","name":"web_search"}],
		"messages":[{"role":"user","content":[{"type":"text","text":"Perform a web search for the query: snowflake cortex agents web_search\nUse the results to answer."}]}]
	}`)
	q, ok := websearch.DetectSearchTurn(body)
	if !ok {
		t.Fatal("Claude Code search sub-turn not detected")
	}
	if q.Text != "snowflake cortex agents web_search" {
		t.Fatalf("query = %q", q.Text)
	}
}

func TestDetectSearchTurnIgnoresConversation(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"web_search_20250305","name":"web_search"}],
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"hello"},
			{"role":"user","content":"Perform a web search for the query: go generics"}
		]
	}`)
	if _, ok := websearch.DetectSearchTurn(body); ok {
		t.Fatal("a multi-message conversation must stay on normal routing")
	}
}

func TestDetectSearchTurnRequiresSearchIntent(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"web_search_20250305","name":"web_search"}],
		"messages":[{"role":"user","content":"refactor this function for me"}]
	}`)
	if _, ok := websearch.DetectSearchTurn(body); ok {
		t.Fatal("a normal first turn that merely offers the tool must not be intercepted")
	}
}

func TestDetectSearchTurnHonorsForcedToolChoice(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"web_search_20250305","name":"web_search"}],
		"tool_choice":{"type":"tool","name":"web_search"},
		"messages":[{"role":"user","content":"latest go release notes"}]
	}`)
	q, ok := websearch.DetectSearchTurn(body)
	if !ok || q.Text != "latest go release notes" {
		t.Fatalf("forced tool_choice turn: ok=%v query=%q", ok, q.Text)
	}
}

func TestDetectSearchTurnIgnoresChoiceForcingAnotherTool(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"Bash"}],
		"tool_choice":{"type":"tool","name":"Bash"},
		"messages":[{"role":"user","content":"list the files here"}]
	}`)
	if _, ok := websearch.DetectSearchTurn(body); ok {
		t.Fatal("a turn forcing a different tool must route normally")
	}
}

func TestSynthesizeMessagePreservesClientToolName(t *testing.T) {
	resp := websearch.Response{
		Summary: "Cortex Agents expose a native web_search tool.",
		Results: []websearch.Result{
			{Title: "Cortex Agents Run API", URL: "https://docs.snowflake.com/agents-run", Snippet: "agent:run accepts a web_search tool_spec."},
			{URL: "https://docs.snowflake.com/agents-manage"},
		},
	}
	msg := websearch.SynthesizeMessage("msg_1", "claude-sonnet-5", "web_search_custom",
		websearch.Query{Text: "cortex agents"}, resp, 120)

	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed := gjson.ParseBytes(raw)
	if got := parsed.Get("content.0.type").String(); got != "server_tool_use" {
		t.Fatalf("first block = %q", got)
	}
	if got := parsed.Get("content.0.name").String(); got != "web_search_custom" {
		t.Fatalf("tool name = %q; a renamed tool must survive so the client renders the result", got)
	}
	if got := parsed.Get("content.0.input.query").String(); got != "cortex agents" {
		t.Fatalf("query = %q", got)
	}
	if got := parsed.Get("content.1.tool_use_id").String(); got != parsed.Get("content.0.id").String() {
		t.Fatalf("result block references %q, tool use is %q", got, parsed.Get("content.0.id").String())
	}
	if got := parsed.Get("content.1.content.#").Int(); got != 2 {
		t.Fatalf("result count = %d", got)
	}
	if got := parsed.Get("content.1.content.0.type").String(); got != "web_search_result" {
		t.Fatalf("result block type = %q", got)
	}
	if got := parsed.Get("content.1.content.1.title").String(); got != "https://docs.snowflake.com/agents-manage" {
		t.Fatalf("untitled hit should fall back to its URL, got %q", got)
	}
	text := parsed.Get("content.2.text").String()
	if !strings.Contains(text, "native web_search tool") || !strings.Contains(text, "https://docs.snowflake.com/agents-run") {
		t.Fatalf("answer text lost the summary or its sources: %q", text)
	}
	if got := parsed.Get("usage.server_tool_use.web_search_requests").Int(); got != 1 {
		t.Fatalf("server tool usage = %d", got)
	}
	if got := parsed.Get("usage.input_tokens").Int(); got != 120 {
		t.Fatalf("input tokens = %d", got)
	}
}

func TestSynthesizeMessageWithoutResults(t *testing.T) {
	msg := websearch.SynthesizeMessage("msg_2", "claude-sonnet-5", "",
		websearch.Query{Text: "nothing"}, websearch.Response{}, 0)
	if got := msg.Content[0].Name; got != "web_search" {
		t.Fatalf("default tool name = %q", got)
	}
	if len(msg.Content[1].Content) != 0 {
		t.Fatalf("expected no result blocks, got %d", len(msg.Content[1].Content))
	}
	if !strings.Contains(msg.TextOf(), "No web search results") {
		t.Fatalf("empty search must say so, got %q", msg.TextOf())
	}
}
