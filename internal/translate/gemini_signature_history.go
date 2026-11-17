package translate

import "github.com/tidwall/gjson"

func (e *RequestEnvelope) HasUnsignedToolCallHistory() bool {
	if e == nil {
		return false
	}
	switch e.format {
	case FormatAnthropic:
		return anthropicHasUnsignedToolUse(e.body)
	case FormatOpenAI:
		return openAIHasUnsignedToolCalls(e.body)
	case FormatGemini:
		return geminiHasUnsignedFunctionCall(e.body)
	default:
		return false
	}
}

func anthropicHasUnsignedToolUse(body []byte) bool {
	found := false
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "assistant" {
			return true
		}
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_use" && extractThoughtSignature(block) == "" {
				found = true
				return false
			}
			return true
		})
		return !found
	})
	return found
}

func openAIHasUnsignedToolCalls(body []byte) bool {
	found := false
	viewJSONPath(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "assistant" {
			return true
		}
		msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
			if extractThoughtSignature(tc) == "" {
				found = true
				return false
			}
			return true
		})
		return !found
	})
	return found
}

func geminiHasUnsignedFunctionCall(body []byte) bool {
	found := false
	gjson.GetBytes(body, "contents").ForEach(func(_, content gjson.Result) bool {
		if content.Get("role").String() != "model" {
			return true
		}
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionCall").Exists() && part.Get("thoughtSignature").String() == "" {
				found = true
				return false
			}
			return true
		})
		return !found
	})
	return found
}
