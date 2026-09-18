package translate

import "github.com/tidwall/gjson"

type responsesEventType string

const (
	responsesCreated                    responsesEventType = "response.created"
	responsesCompleted                  responsesEventType = "response.completed"
	responsesIncomplete                 responsesEventType = "response.incomplete"
	responsesFailed                     responsesEventType = "response.failed"
	responsesFunctionCallArgumentsDelta responsesEventType = "response.function_call_arguments.delta"
	responsesCustomToolCallInputDelta   responsesEventType = "response.custom_tool_call_input.delta"
	responsesOutputTextDelta            responsesEventType = "response.output_text.delta"
	responsesOutputTextDone             responsesEventType = "response.output_text.done"
	responsesRefusalDelta               responsesEventType = "response.refusal.delta"
	responsesRefusalDone                responsesEventType = "response.refusal.done"
	responsesContentPartAdded           responsesEventType = "response.content_part.added"
	responsesContentPartDone            responsesEventType = "response.content_part.done"
	responsesReasoningSummaryDone       responsesEventType = "response.reasoning_summary_text.done"
	responsesReasoningSummaryPartAdded  responsesEventType = "response.reasoning_summary_part.added"
	responsesReasoningSummaryPartDone   responsesEventType = "response.reasoning_summary_part.done"
	responsesOutputItemAdded            responsesEventType = "response.output_item.added"
	responsesOutputItemDone             responsesEventType = "response.output_item.done"
	responsesReasoningSummaryDelta      responsesEventType = "response.reasoning_summary_text.delta"
	responsesReasoningTextDelta         responsesEventType = "response.reasoning_text.delta"
)

type responsesItemType string

const (
	responsesReasoningItem      responsesItemType = "reasoning"
	responsesMessageItem        responsesItemType = "message"
	responsesFunctionCallItem   responsesItemType = "function_call"
	responsesCustomToolCallItem responsesItemType = "custom_tool_call"
	responsesComputerCallItem   responsesItemType = "computer_call"
)

type responsesContentType string

const (
	responsesOutputText responsesContentType = "output_text"
	responsesRefusal    responsesContentType = "refusal"
)

// Both translators use the same completed-item gate, including opaque reasoning
// that Chat cannot render. An added item's encrypted content may be incomplete.
func completedReasoningHasProgress(item gjson.Result) bool {
	id, encrypted := item.Get("id"), item.Get("encrypted_content")
	if id.Type == gjson.String && id.Str != "" && encrypted.Type == gjson.String && encrypted.Str != "" {
		return true
	}
	for _, summary := range item.Get("summary").Array() {
		if text := summary.Get("text"); text.Type == gjson.String && text.Str != "" {
			return true
		}
	}
	return false
}
