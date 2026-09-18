package translate

import (
	"errors"
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

func upstreamErrorHTTPStatus(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var buffered *providers.UpstreamErrorResponse
	if errors.As(err, &buffered) && buffered.Status >= 400 {
		return buffered.Status, true
	}
	var status *providers.UpstreamStatusError
	if errors.As(err, &status) && status.Status >= 400 {
		return status.Status, true
	}
	return 0, false
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
		if responsesItemHasUsableOutput(item) {
			return true
		}
	}
	return false
}

func responsesItemHasUsableOutput(item gjson.Result) bool {
	switch responsesItemType(item.Get("type").String()) {
	case responsesMessageItem:
		for _, part := range item.Get("content").Array() {
			if responsesContentType(part.Get("type").String()) == responsesOutputText && part.Get("text").String() != "" {
				return true
			}
		}
	case responsesFunctionCallItem:
		return item.Get("name").String() != ""
	case responsesReasoningItem:
		return joinReasoningSummary(item.Get("summary")) != ""
	}
	return false
}

type nativeResponsesStatus string

const (
	nativeResponsesStatusCompleted  nativeResponsesStatus = "completed"
	nativeResponsesStatusIncomplete nativeResponsesStatus = "incomplete"
	nativeResponsesStatusQueued     nativeResponsesStatus = "queued"
	nativeResponsesStatusInProgress nativeResponsesStatus = "in_progress"
	nativeResponsesStatusFailed     nativeResponsesStatus = "failed"
	nativeResponsesStatusCancelled  nativeResponsesStatus = "cancelled"
)

func nativeResponsesStatusIsAnswerTerminal(status string) bool {
	switch nativeResponsesStatus(status) {
	case nativeResponsesStatusCompleted, nativeResponsesStatusIncomplete:
		return true
	case "":
		// Terminal SSE events sometimes omit status; treat them as answer terminals.
		return true
	default:
		return false
	}
}

func nativeResponsesIsEmptyTerminal(resp gjson.Result) bool {
	if !resp.Get("output").IsArray() {
		return false
	}
	if !nativeResponsesStatusIsAnswerTerminal(resp.Get("status").String()) {
		return false
	}
	return !nativeResponsesResponseHasUsableOutput(resp)
}

func nativeResponsesResponseHasUsableOutput(resp gjson.Result) bool {
	for _, item := range resp.Get("output").Array() {
		if nativeResponsesItemHasUsableOutput(item) {
			return true
		}
	}
	return false
}

func nativeResponsesItemHasUsableOutput(item gjson.Result) bool {
	if responsesItemHasUsableOutput(item) {
		return true
	}
	switch responsesItemType(item.Get("type").String()) {
	case responsesCustomToolCallItem:
		return item.Get("name").String() != ""
	case responsesComputerCallItem:
		return item.Get("call_id").String() != "" || item.Get("action").Exists()
	case responsesMessageItem:
		for _, part := range item.Get("content").Array() {
			if nativeResponsesContentHasUsableOutput(part) {
				return true
			}
		}
	}
	return false
}

func nativeResponsesContentHasUsableOutput(part gjson.Result) bool {
	switch responsesContentType(part.Get("type").String()) {
	case responsesOutputText:
		return part.Get("text").String() != ""
	case responsesRefusal:
		return part.Get("refusal").String() != ""
	}
	return false
}
