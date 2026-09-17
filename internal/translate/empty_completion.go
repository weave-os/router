package translate

import (
	"net/http"
	"strings"

	"weave-os/router/internal/providers"

	"github.com/tidwall/gjson"
)

const (
	upstreamEmptyCompletionType    = "upstream_empty_completion"
	upstreamEmptyCompletionMessage = "Upstream returned no assistant output."
)

func newEmptyCompletionError(body []byte) error {
	return &providers.UpstreamErrorResponse{
		Status: http.StatusBadGateway,
		Body:   body,
		Cause:  providers.ErrUpstreamEmptyCompletion,
	}
}

func emptyCompletionOpenAIError() error {
	return newEmptyCompletionError(openAIErrorBody(upstreamEmptyCompletionType, upstreamEmptyCompletionMessage))
}

func emptyCompletionAnthropicError() error {
	return newEmptyCompletionError(responsesError(upstreamEmptyCompletionType, upstreamEmptyCompletionMessage))
}

func chatCompletionHasUsableOutput(body []byte) bool {
	choice := gjson.GetBytes(body, "choices.0.message")
	if chatContentHasText(choice.Get("content")) {
		return true
	}
	for _, toolCall := range choice.Get("tool_calls").Array() {
		if toolCall.Get("function.name").String() != "" {
			return true
		}
	}
	return false
}

func chatCompletionHasReasoningOutput(body []byte) bool {
	reasoning := gjson.GetBytes(body, "choices.0.message.reasoning_content")
	return reasoning.Type == gjson.String && reasoning.String() != ""
}

func chatContentHasText(content gjson.Result) bool {
	return chatContentText(content) != ""
}

func chatContentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var text strings.Builder
	for _, part := range content.Array() {
		if part.Get("text").Type == gjson.String && part.Get("text").String() != "" {
			text.WriteString(part.Get("text").String())
		}
	}
	return text.String()
}

func anthropicResponseHasUsableOutput(body []byte) bool {
	for _, block := range gjson.GetBytes(body, "content").Array() {
		switch block.Get("type").String() {
		case "text", "tool_use":
			if block.Get("text").String() != "" || block.Get("name").String() != "" {
				return true
			}
		case "thinking":
			if block.Get("thinking").String() != "" || block.Get("signature").String() != "" {
				return true
			}
		}
	}
	return false
}

func responsesResponseHasUsableOutput(resp gjson.Result) bool {
	for _, item := range resp.Get("output").Array() {
		switch item.Get("type").String() {
		case "message":
			for _, part := range item.Get("content").Array() {
				if part.Get("type").String() == "output_text" && part.Get("text").String() != "" {
					return true
				}
			}
		case "function_call":
			if item.Get("name").String() != "" {
				return true
			}
		case "reasoning":
			if joinReasoningSummary(item.Get("summary")) != "" {
				return true
			}
		}
	}
	return false
}

func nativeResponsesResponseHasUsableOutput(resp gjson.Result) bool {
	if responsesResponseHasUsableOutput(resp) {
		return true
	}
	for _, item := range resp.Get("output").Array() {
		if item.Get("type").String() == "custom_tool_call" && item.Get("name").String() != "" {
			return true
		}
	}
	return false
}
