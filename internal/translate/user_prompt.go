package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

// EndsWithUserPrompt reports whether the trailing conversational turn is text
// a person typed: a user message with visible text that answers no tool call.
// Out-of-band role:"system" notices are skipped, and the wrapper blocks Claude
// Code injects (<system-reminder>, <command-name>, ...) are not visible text,
// so harness follow-ups that only carry those do not count. Any other tagged
// text is the person's own. Anthropic, OpenAI, and Gemini shapes.
func (e *RequestEnvelope) EndsWithUserPrompt() bool {
	if e.format == FormatGemini {
		return geminiEndsWithUserPrompt(e.body)
	}
	switch e.format {
	case FormatAnthropic, FormatOpenAI:
	default:
		return false
	}
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return false
	}
	all := msgs.Array()
	sawTypedText := false
	for i := len(all) - 1; i >= 0; i-- {
		switch all[i].Get("role").String() {
		case "user":
			answersToolCall, text := userTurnText(all[i].Get("content"))
			if answersToolCall {
				return false
			}
			if !isOnlyKnownInjectedText(text) {
				sawTypedText = true
			}
		case "tool":
			return false
		case "assistant":
			return sawTypedText && !assistantCallsTools(all[i])
		}
	}
	return sawTypedText
}

func geminiEndsWithUserPrompt(body []byte) bool {
	contents := gjson.GetBytes(body, "contents").Array()
	sawTypedText := false
	for i := len(contents) - 1; i >= 0; i-- {
		if contents[i].Get("role").String() == "model" {
			return sawTypedText
		}
		for _, part := range contents[i].Get("parts").Array() {
			if part.Get("functionResponse").Exists() {
				return false
			}
			if !isOnlyKnownInjectedText(part.Get("text").String()) {
				sawTypedText = true
			}
		}
	}
	return sawTypedText
}

// userTurnText returns whether a user message carries a tool result and the
// concatenation of its text blocks.
func userTurnText(content gjson.Result) (answersToolCall bool, text string) {
	if content.Type == gjson.String {
		return false, content.String()
	}
	if !content.IsArray() {
		return false, ""
	}
	var b strings.Builder
	content.ForEach(func(_, block gjson.Result) bool {
		switch block.Get("type").String() {
		case "tool_result":
			answersToolCall = true
			return false
		case "text":
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(block.Get("text").String())
		}
		return true
	})
	return answersToolCall, b.String()
}

// assistantCallsTools reports whether an assistant message requested tool
// calls, in either the OpenAI tool_calls or the Anthropic tool_use shape.
func assistantCallsTools(assistant gjson.Result) bool {
	if toolCalls := assistant.Get("tool_calls"); toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
		return true
	}
	content := assistant.Get("content")
	if !content.IsArray() {
		return false
	}
	for _, block := range content.Array() {
		if block.Get("type").String() == "tool_use" {
			return true
		}
	}
	return false
}

// LatestToolCallOutcomes returns the outcomes of the most recent assistant
// message's tool_use blocks: the calls whose results this request delivers.
// Earlier assistant messages are excluded. Client retries can deliver the
// same results again; these are observations, not distinct executions.
// Anthropic format only.
func (e *RequestEnvelope) LatestToolCallOutcomes() []ToolCallOutcome {
	if e.format != FormatAnthropic {
		return nil
	}
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return nil
	}
	all := msgs.Array()
	latest := -1
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Get("role").String() == "assistant" {
			latest = i
			break
		}
	}
	if latest < 0 {
		return nil
	}
	content := all[latest].Get("content")
	if !content.IsArray() {
		return nil
	}
	errored := toolResultOutcomesByID(msgs)
	var out []ToolCallOutcome
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != "tool_use" {
			return true
		}
		name := block.Get("name").String()
		if name == "" {
			return true
		}
		isErr, resolved := errored[block.Get("id").String()]
		out = append(out, ToolCallOutcome{Name: name, Resolved: resolved, Errored: isErr})
		return true
	})
	return out
}
