package translate

import (
	"github.com/tidwall/gjson"
	"weave-os/router/internal/sse"
)

// A completed empty message is an end-of-turn, not a transport failure. Returning
// it lets the client add a continuation instead of retrying identical input.
// Never use this exception for output the translator may have discarded.
type completedEmptyTurn struct {
	disqualified bool
	terminals    int
}

func (s *completedEmptyTurn) observe(data []byte) {
	if s.disqualified {
		return
	}
	if string(data) == "[DONE]" && s.terminals == 1 {
		return
	}
	if !gjson.ValidBytes(data) {
		s.disqualified = true
		return
	}
	event := gjson.ParseBytes(data)
	kind := responsesEventType(event.Get("type").String())
	if nonNullResponseField(event, "error") || nonNullResponseField(event, "incomplete_details") {
		s.disqualified = true
		return
	}
	if emptyTurnControlMetadata(kind) {
		for _, field := range []string{"output", "item", "part", "delta", "text", "content", "arguments", "refusal", "response"} {
			if event.Get(field).Exists() {
				s.disqualified = true
				return
			}
		}
		return
	}
	if s.terminals > 0 {
		s.disqualified = true
	}
	switch kind {
	case responsesCompleted:
		s.terminals++
		r := event.Get("response")
		output := r.Get("output")
		if r.Get("status").String() != "completed" || nonNullResponseField(r, "error") || nonNullResponseField(r, "incomplete_details") || !output.IsArray() || len(output.Array()) != 1 || !emptyAssistantMessage(output.Array()[0], false) {
			s.disqualified = true
		}
	case responsesCreated, "response.in_progress":
		r := event.Get("response")
		if nonNullResponseField(r, "error") || nonNullResponseField(r, "incomplete_details") || !r.Get("output").IsArray() || len(r.Get("output").Array()) != 0 {
			s.disqualified = true
		}
	case responsesOutputItemAdded, responsesOutputItemDone:
		if !emptyAssistantMessage(event.Get("item"), kind == responsesOutputItemAdded) {
			s.disqualified = true
		}
	case responsesContentPartAdded, responsesContentPartDone:
		if !emptyOutputText(event.Get("part")) {
			s.disqualified = true
		}
	case responsesOutputTextDelta:
		if d := event.Get("delta"); d.Type != gjson.String || d.Str != "" {
			s.disqualified = true
		}
	case responsesOutputTextDone:
		if d := event.Get("text"); d.Type != gjson.String || d.Str != "" {
			s.disqualified = true
		}
	default:
		s.disqualified = true
	}
}

func (s *completedEmptyTurn) accepted() bool { return s.terminals == 1 && !s.disqualified }

func nonNullResponseField(r gjson.Result, key string) bool {
	v := r.Get(key)
	return v.Exists() && v.Type != gjson.Null
}

func emptyAssistantMessage(item gjson.Result, allowInProgress bool) bool {
	if item.Get("type").String() != "message" || item.Get("role").String() != "assistant" {
		return false
	}
	status := item.Get("status").String()
	if status != "completed" && !(allowInProgress && status == "in_progress") {
		return false
	}
	content := item.Get("content")
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		if !emptyOutputText(part) {
			return false
		}
	}
	return true
}

func emptyOutputText(part gjson.Result) bool {
	text := part.Get("text")
	annotations := part.Get("annotations")
	return part.Get("type").String() == "output_text" && text.Type == gjson.String && text.Str == "" && (!annotations.Exists() || (annotations.IsArray() && len(annotations.Array()) == 0))
}

func bufferedCompletedEmptyTurn(body []byte) completedEmptyTurn {
	var state completedEmptyTurn
	for {
		event, n := splitBufferedResponsesEvent(body)
		if n == 0 {
			break
		}
		body = body[n:]
		_, data := sse.ParseEvent(event)
		if len(data) > 0 {
			state.observe(data)
		}
	}
	return state
}

// These subscription-stream events carry control metadata, never assistant output.
func emptyTurnControlMetadata(kind responsesEventType) bool {
	switch kind {
	case "codex.rate_limits", "codex.response.metadata", "response.metadata", "responsesapi.websocket_timing":
		return true
	default:
		return false
	}
}
