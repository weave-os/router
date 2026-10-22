package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

// openAIMaxTools bounds the function tools emitted to OpenAI-family upstreams.
const openAIMaxTools = 128

// selectOpenAIToolIndexes returns the indexes (ascending) of the tools to emit
// when names exceeds openAIMaxTools; nil means emit all. Claude Code appends
// MCP tools after its built-ins, so a positional cut silently drops exactly the
// MCP tools a session is using and the model then calls them blind. Priority:
// tools the conversation references (most recent first), other tools on the
// same MCP servers, then the rest in request order.
func selectOpenAIToolIndexes(names []string, referenced []string) []int {
	if len(names) <= openAIMaxTools {
		return nil
	}
	byName := make(map[string]int, len(names))
	for i, name := range names {
		if _, seen := byName[name]; !seen {
			byName[name] = i
		}
	}
	keep := make(map[int]struct{}, openAIMaxTools)
	servers := make(map[string]struct{})
	for i := len(referenced) - 1; i >= 0 && len(keep) < openAIMaxTools; i-- {
		if idx, ok := byName[referenced[i]]; ok {
			keep[idx] = struct{}{}
			if server := mcpServerPrefix(referenced[i]); server != "" {
				servers[server] = struct{}{}
			}
		}
	}
	for i := 0; i < len(names) && len(keep) < openAIMaxTools && len(servers) > 0; i++ {
		if _, ok := servers[mcpServerPrefix(names[i])]; ok {
			keep[i] = struct{}{}
		}
	}
	for i := 0; i < len(names) && len(keep) < openAIMaxTools; i++ {
		keep[i] = struct{}{}
	}
	out := make([]int, 0, len(keep))
	for i := range names {
		if _, ok := keep[i]; ok {
			out = append(out, i)
		}
	}
	return out
}

// mcpServerPrefix returns "mcp__<server>__" for a Claude Code MCP tool name.
func mcpServerPrefix(name string) string {
	const prefix = "mcp__"
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	end := strings.Index(name[len(prefix):], "__")
	if end <= 0 {
		return ""
	}
	return name[:len(prefix)+end+2]
}

// anthropicReferencedToolNames lists tool names the Anthropic request already
// depends on, in priority order (last is highest): every tool_use and
// ToolSearch tool_reference in history, then the forced tool_choice.
func anthropicReferencedToolNames(body []byte) []string {
	var names []string
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		collectAnthropicReferencedToolNames(msg.Get("content"), &names)
		return true
	})
	if kind, name := anthropicToolChoice(body); kind == toolChoiceNamed {
		names = append(names, name)
	}
	return names
}

func collectAnthropicReferencedToolNames(blocks gjson.Result, names *[]string) {
	blocks.ForEach(func(_, block gjson.Result) bool {
		switch anthropicToolNameBlock(block.Get("type").String()) {
		case anthropicToolNameUse:
			if name := block.Get("name").String(); name != "" {
				*names = append(*names, name)
			}
		case anthropicToolNameReference:
			name := block.Get("tool_name").String()
			if name == "" {
				name = block.Get("name").String()
			}
			if name != "" {
				*names = append(*names, name)
			}
		case anthropicToolNameResult:
			collectAnthropicReferencedToolNames(block.Get("content"), names)
		}
		return true
	})
}

// openAIReferencedToolNames is the chat/completions counterpart of
// anthropicReferencedToolNames.
func openAIReferencedToolNames(body []byte) []string {
	var names []string
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
			if name := call.Get("function.name").String(); name != "" {
				names = append(names, name)
			}
			return true
		})
		return true
	})
	if kind, name := openAIToolChoice(body); kind == toolChoiceNamed {
		names = append(names, name)
	}
	return names
}
