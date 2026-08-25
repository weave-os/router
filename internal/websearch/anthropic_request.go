package websearch

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// serverToolPrefix marks Anthropic's dated native web-search server tools
// (web_search_20250305, web_search_20260209, and whatever they date next).
// Client-executed function tools carry no type, so the prefix is sufficient.
const serverToolPrefix = "web_search_"

// claudeCodeSearchPrompt is the sub-turn Claude Code sends when its WebSearch
// tool fires: a single user message whose text is this prefix plus the query.
const claudeCodeSearchPrompt = "Perform a web search for the query: "

// ServerTool is an inbound native web-search tool declaration.
type ServerTool struct {
	// Type is the dated tool type, e.g. "web_search_20250305".
	Type string
	// Name is the logical name the model calls and the client renders,
	// normally "web_search". Preserved verbatim: a client keyed off its own
	// name drops result blocks announced under a different one.
	Name string
}

// FindServerTool returns the first native web-search server tool declared in
// an Anthropic Messages body.
func FindServerTool(body []byte) (ServerTool, bool) {
	var found ServerTool
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		toolType := tool.Get("type").String()
		if !strings.HasPrefix(toolType, serverToolPrefix) {
			return true
		}
		name := tool.Get("name").String()
		if name == "" {
			name = "web_search"
		}
		found = ServerTool{Type: toolType, Name: name}
		return false
	})
	return found, found.Type != ""
}

// StripServerTools removes native web-search tools from an Anthropic Messages
// body and reports how many it removed. Used to keep a turn alive on an
// upstream that rejects the tool outright instead of failing the whole turn.
func StripServerTools(body []byte) ([]byte, int) {
	removed := 0
	// Reverse order: deleting by index shifts every later element.
	tools := gjson.GetBytes(body, "tools").Array()
	for i := len(tools) - 1; i >= 0; i-- {
		if !strings.HasPrefix(tools[i].Get("type").String(), serverToolPrefix) {
			continue
		}
		out, err := sjson.DeleteBytes(body, "tools."+strconv.Itoa(i))
		if err != nil {
			continue
		}
		body = out
		removed++
	}
	if removed == 0 {
		return body, 0
	}
	if len(gjson.GetBytes(body, "tools").Array()) == 0 {
		if out, err := sjson.DeleteBytes(body, "tools"); err == nil {
			body = out
		}
		if out, err := sjson.DeleteBytes(body, "tool_choice"); err == nil {
			body = out
		}
	}
	return body, removed
}

// DetectSearchTurn returns the query of a self-contained web-search turn: one
// user message whose only purpose is to run a search, with the native tool
// declared. Anything longer is a real conversation that happens to offer the
// tool, and must keep going to the model.
func DetectSearchTurn(body []byte) (Query, bool) {
	tool, ok := FindServerTool(body)
	if !ok {
		return Query{}, false
	}
	messages := gjson.GetBytes(body, "messages").Array()
	if len(messages) != 1 || messages[0].Get("role").String() != "user" {
		return Query{}, false
	}
	text := strings.TrimSpace(userText(messages[0]))
	if text == "" {
		return Query{}, false
	}
	// A forced choice only counts when it names the search tool; forcing some
	// other tool is an ordinary turn that happens to also declare web_search.
	forced := gjson.GetBytes(body, "tool_choice.type").String() == "tool" &&
		gjson.GetBytes(body, "tool_choice.name").String() == tool.Name
	trimmed, prompted := cutSearchPrompt(text)
	if !forced && !prompted {
		return Query{}, false
	}
	return Query{Text: trimmed}, true
}

// cutSearchPrompt strips Claude Code's search preamble, reporting whether the
// text carried it.
func cutSearchPrompt(text string) (string, bool) {
	idx := strings.Index(text, claudeCodeSearchPrompt)
	if idx < 0 {
		return text, false
	}
	query := strings.TrimSpace(text[idx+len(claudeCodeSearchPrompt):])
	if query == "" {
		return text, false
	}
	// Claude Code appends usage guidance after the query on its own line.
	if nl := strings.IndexByte(query, '\n'); nl >= 0 {
		query = strings.TrimSpace(query[:nl])
	}
	return strings.Trim(query, `"`), true
}

// userText concatenates the text of a message whose content is either a bare
// string or Anthropic's content-block array.
func userText(message gjson.Result) string {
	content := message.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	var b strings.Builder
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != "text" {
			return true
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(block.Get("text").String())
		return true
	})
	return b.String()
}
