package translate

import (
	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
)

func (t *ResponsesWriter) validateNativeResponsesEvent(event gjson.Result) error {
	// Inspect provider content before badges or footers can make an empty turn
	// look usable. Some providers omit already streamed output from the terminal.
	if nativeResponsesEventHasUsableOutput(event) {
		t.hasUpstreamOutput = true
	}
	switch responsesEventType(event.Get("type").Str) {
	case responsesCompleted, responsesIncomplete:
		if !t.hasUpstreamOutput && nativeResponsesIsEmptyTerminal(event.Get("response")) {
			t.nativeEmptyRejected = true
			return emptyCompletionOpenAIError()
		}
	}
	return nil
}

func nativeResponsesEventHasUsableOutput(event gjson.Result) bool {
	switch responsesEventType(event.Get("type").Str) {
	case responsesOutputTextDelta, responsesRefusalDelta, responsesReasoningSummaryDelta:
		return event.Get("delta").Str != ""
	case responsesOutputTextDone, responsesReasoningSummaryDone:
		return event.Get("text").Str != ""
	case responsesRefusalDone:
		return event.Get("refusal").Str != ""
	case responsesContentPartAdded, responsesContentPartDone:
		return nativeResponsesContentHasUsableOutput(event.Get("part"))
	case responsesReasoningSummaryPartAdded, responsesReasoningSummaryPartDone:
		return event.Get("part.text").Str != ""
	case responsesOutputItemAdded, responsesOutputItemDone:
		return nativeResponsesItemHasUsableOutput(event.Get("item"))
	}
	return false
}

func (t *ResponsesWriter) writeNativeResponsesFrame(event, delimiter []byte) error {
	if _, err := t.bw.Write(event); err != nil {
		return err
	}
	if _, err := t.bw.Write(delimiter); err != nil {
		return err
	}
	t.nativeStreamStarted = true
	_, payload := sse.ParseEvent(event)
	if !gjson.ValidBytes(payload) {
		return nil
	}
	root := gjson.ParseBytes(payload)
	if id := root.Get("response.id").Str; id != "" {
		t.nativeResponseID = id
	}
	// Held footer events may be written after events with higher sequences.
	if sequence := root.Get("sequence_number"); sequence.Type == gjson.Number && (!t.nativeLastSequenceSet || sequence.Int() > t.nativeLastSequence) {
		t.nativeLastSequence = sequence.Int()
		t.nativeLastSequenceSet = true
	}
	switch responsesEventType(root.Get("type").Str) {
	case responsesCompleted, responsesIncomplete, responsesFailed:
		t.completedEmitted = true
	}
	return nil
}
