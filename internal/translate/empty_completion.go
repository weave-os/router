package translate

import (
	"net/http"

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
	return newEmptyCompletionError(responsesErrorBody(upstreamEmptyCompletionType, upstreamEmptyCompletionMessage))
}

func chatCompletionHasUsableOutput(body []byte) bool {
	choice := gjson.GetBytes(body, "choices.0.message")
	if choice.Get("content").Type == gjson.String && choice.Get("content").String() != "" {
		return true
	}
	if choice.Get("reasoning_content").Type == gjson.String && choice.Get("reasoning_content").String() != "" {
		return true
	}
	for _, toolCall := range choice.Get("tool_calls").Array() {
		if toolCall.Get("function.name").String() != "" {
			return true
		}
	}
	return false
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
		case "function_call", "custom_tool_call":
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
