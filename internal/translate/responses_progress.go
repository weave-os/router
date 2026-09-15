package translate

import "github.com/tidwall/gjson"

type responsesProgressEvent string

const (
	responsesOutputItemAdded       responsesProgressEvent = "response.output_item.added"
	responsesOutputItemDone        responsesProgressEvent = "response.output_item.done"
	responsesReasoningSummaryDelta responsesProgressEvent = "response.reasoning_summary_text.delta"
	responsesReasoningTextDelta    responsesProgressEvent = "response.reasoning_text.delta"
)

type responsesProgressItem string

const responsesReasoningItem responsesProgressItem = "reasoning"

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
