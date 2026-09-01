package translate

import (
	"strings"

	"workweave/router/internal/websearch"

	"github.com/tidwall/gjson"
)

// SearchToolUseRecency reports how many assistant turns have elapsed since the last actual
// web-search/fetch tool invocation (not mere advertisement): 0 = current turn,
// N = N turns ago, -1 = no search use in history.
func (e *RequestEnvelope) SearchToolUseRecency() int {
	switch e.format {
	case FormatAnthropic:
		if _, ok := websearch.DetectSearchTurn(e.body); ok {
			return 0
		}
		return searchUseRecency(gjson.GetBytes(e.body, "messages"), anthropicSearchUse)
	case FormatOpenAI:
		items := gjson.GetBytes(e.body, "messages")
		if !items.Exists() {
			items = gjson.GetBytes(e.body, "input")
		}
		return searchUseRecency(items, openAISearchUse)
	default:
		return -1
	}
}

// searchUseRecency walks the conversation in order, counting assistant turns
// after the most recent item usedFn matches.
func searchUseRecency(items gjson.Result, usedFn func(gjson.Result) bool) int {
	recency := -1
	items.ForEach(func(_, item gjson.Result) bool {
		if usedFn(item) {
			recency = 0
			return true
		}
		if recency >= 0 && item.Get("role").String() == "assistant" {
			recency++
		}
		return true
	})
	return recency
}

func anthropicSearchUse(message gjson.Result) bool {
	used := false
	message.Get("content").ForEach(func(_, block gjson.Result) bool {
		switch block.Get("type").String() {
		case "server_tool_use", "web_search_tool_result", "web_fetch_tool_result":
			used = true
		case "tool_use":
			name := block.Get("name").String()
			used = name == "WebSearch" || name == "WebFetch"
		}
		return !used
	})
	return used
}

func openAISearchUse(item gjson.Result) bool {
	return strings.HasPrefix(item.Get("type").String(), "web_search_call")
}
