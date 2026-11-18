package websearch

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const serverToolPrefix = "web_search_"

const claudeCodeSearchPrompt = "Perform a web search for the query: "
const claudeCodeSearchSystemPrompt = "You are an assistant for performing a web search tool use"
const claudeCodeSearchSystemPromptCurrent = "You run a web search for another Claude model, which is waiting on the result and can search again. Search once, then reply with a brief summary of the results."

func isClaudeCodeSearchSystemPrompt(prompt string) bool {
	switch prompt {
	case claudeCodeSearchSystemPrompt, claudeCodeSearchSystemPromptCurrent:
		return true
	default:
		return false
	}
}

func IsClaudeCodeWebSearchHelper(body []byte) bool {
	if len(gjson.GetBytes(body, "tools").Array()) != 1 {
		return false
	}
	if _, search := DetectSearchTurn(body); !search {
		return false
	}
	system := gjson.GetBytes(body, "system")
	if system.Type == gjson.String {
		return isClaudeCodeSearchSystemPrompt(system.String())
	}
	blocks := system.Array()
	return len(blocks) > 0 && isClaudeCodeSearchSystemPrompt(blocks[len(blocks)-1].Get("text").String())
}

type ServerTool struct {
	Type string
	Name string
}

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

func HasDomainFilter(body []byte) bool {
	tool := gjson.GetBytes(body, "tools.0")
	return len(tool.Get("allowed_domains").Array()) > 0 || len(tool.Get("blocked_domains").Array()) > 0
}

var nativeServerToolPrefixes = []string{serverToolPrefix, "web_fetch_"}

func isNativeServerTool(toolType string) bool {
	for _, prefix := range nativeServerToolPrefixes {
		if strings.HasPrefix(toolType, prefix) {
			return true
		}
	}
	return false
}

func StripServerTools(body []byte) ([]byte, int) {
	removed := 0
	strippedNames := make(map[string]struct{})
	tools := gjson.GetBytes(body, "tools").Array()
	for i := len(tools) - 1; i >= 0; i-- {
		tool := tools[i]
		if !isNativeServerTool(tool.Get("type").String()) {
			continue
		}
		name := tool.Get("name").String()
		out, err := sjson.DeleteBytes(body, "tools."+strconv.Itoa(i))
		if err != nil {
			continue
		}
		body = out
		removed++
		if name != "" {
			strippedNames[name] = struct{}{}
		}
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
	} else if gjson.GetBytes(body, "tool_choice.type").String() == "tool" {
		choiceName := gjson.GetBytes(body, "tool_choice.name").String()
		if _, ok := strippedNames[choiceName]; ok {
			if out, err := sjson.DeleteBytes(body, "tool_choice"); err == nil {
				body = out
			}
		}
	}
	return body, removed
}

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
	forced := gjson.GetBytes(body, "tool_choice.type").String() == "tool" &&
		gjson.GetBytes(body, "tool_choice.name").String() == tool.Name
	trimmed, prompted := cutSearchPrompt(text)
	if !forced && !prompted {
		return Query{}, false
	}
	return Query{Text: trimmed}, true
}

func cutSearchPrompt(text string) (string, bool) {
	idx := strings.Index(text, claudeCodeSearchPrompt)
	if idx < 0 {
		return text, false
	}
	query := strings.TrimSpace(text[idx+len(claudeCodeSearchPrompt):])
	if query == "" {
		return text, false
	}
	if nl := strings.IndexByte(query, '\n'); nl >= 0 {
		query = strings.TrimSpace(query[:nl])
	}
	return strings.Trim(query, `"`), true
}

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
