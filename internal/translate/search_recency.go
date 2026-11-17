package translate

import (
	"strings"

	"weave-os/router/internal/websearch"

	"github.com/tidwall/gjson"
)

func (e *RequestEnvelope) IsNativeWebSearchSubTurn() bool {
	if e == nil || e.format != FormatAnthropic {
		return false
	}
	_, ok := websearch.DetectSearchTurn(e.body)
	return ok
}

func (e *RequestEnvelope) SearchToolUseRecency() int {
	switch e.format {
	case FormatAnthropic:
		if e.IsNativeWebSearchSubTurn() {
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
