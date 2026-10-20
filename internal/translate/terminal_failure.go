package translate

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"weave-os/router/internal/sse"
)

// TerminalFailure carries resolved delivery meaning. Classification and the
// decision to terminate a request remain with the request owner.
type TerminalFailure struct {
	Status            int
	Message           string
	Type              string
	Code              string
	GeminiStatus      string
	ProviderBody      []byte
	ResponsesSnapshot json.RawMessage
}

func (s *protocolNoticeSink) EmitFailure(failure TerminalFailure, streaming bool) error {
	s.Seal()
	failure = s.adaptFailure(failure)
	if streaming && s.responses != nil {
		writer := s.responses
		writer.failureCode, writer.failureMessage = failure.Code, failure.Message
		writer.failureSnapshot = failure.ResponsesSnapshot
		if !writer.nativeStreamStarted && !writer.nativePreludeCreated {
			writer.streaming = true
			writer.statusCode = http.StatusOK
			writer.inner.Header().Set("Content-Type", "text/event-stream")
			writer.inner.Header().Del("Content-Length")
			writer.inner.Header().Del("Content-Encoding")
			writer.inner.WriteHeader(http.StatusOK)
			writer.httpHeadersSent = true
			if err := writer.writeNativeEvent(string(responsesCreated), 0, map[string]any{"response": writer.responseEnvelope("in_progress")}); err != nil {
				return err
			}
			writer.nativePreludeCreated = true
		}
		if err := writer.FinalizeError(nil); err != nil {
			return err
		}
		writer.Flush()
		return nil
	}
	body, err := s.failureBody(failure)
	if err != nil {
		return err
	}
	var escaped bytes.Buffer
	json.HTMLEscape(&escaped, body)
	body = escaped.Bytes()
	s.client.Header().Del("Content-Length")
	s.client.Header().Del("Content-Encoding")
	s.client.Header().Set("X-Content-Type-Options", "nosniff")
	if !streaming {
		s.client.Header().Set("Content-Type", "application/json")
		s.client.WriteHeader(failure.Status)
		_, err = s.client.Write(body)
		return err
	}
	s.client.Header().Set("Content-Type", "text/event-stream")
	frame := []byte("data: ")
	if s.format == FormatAnthropic {
		frame = []byte("event: error\ndata: ")
	}
	frame = append(frame, body...)
	frame = append(frame, '\n', '\n')
	_, err = s.client.Write(frame)
	if flusher, ok := s.client.(http.Flusher); ok {
		flusher.Flush()
	}
	return err
}

// adaptFailure projects provider envelopes at the client protocol boundary.
func (s *protocolNoticeSink) adaptFailure(failure TerminalFailure) TerminalFailure {
	body := failure.ProviderBody
	if !json.Valid(body) {
		// Some providers return an SSE error record even for an HTTP rejection.
		for len(body) > 0 {
			event, consumed := splitBufferedResponsesEvent(body)
			if consumed == 0 {
				break
			}
			_, payload := sse.ParseEvent(event)
			if json.Valid(payload) && responsesFailurePayload(payload) {
				failure.ProviderBody = bytes.Clone(payload)
				return s.adaptFailure(failure)
			}
			body = body[consumed:]
		}
		return failure
	}
	root := gjson.ParseBytes(body)
	providerError := root.Get("response.error")
	if !providerError.IsObject() {
		providerError = root.Get("error")
	}
	if !providerError.IsObject() {
		providerError = root
	}
	if message := providerError.Get("message").Str; message != "" {
		failure.Message = message
	}
	if code := providerError.Get("code"); code.Exists() && code.Type != gjson.Null {
		failure.Code = code.String()
	}
	if kind := providerError.Get("type").Str; kind != "" {
		failure.Type = kind
	}
	// Responses event wrappers are not HTTP error envelopes.
	if root.Get("response").Exists() || !root.Get("error").IsObject() {
		body, _ = json.Marshal(map[string]json.RawMessage{"error": json.RawMessage(providerError.Raw)})
	}
	nativeGemini := gjson.GetBytes(body, "error.status").Exists()
	nativeAnthropic := root.Get("type").Str == "error" && root.Get("error.type").Exists()
	switch s.format {
	case FormatAnthropic:
		if nativeGemini {
			body = OpenAIToAnthropicError(GeminiToOpenAIError(body))
		} else if !nativeAnthropic {
			body = ResponsesToAnthropicError(body)
		}
	case FormatGemini:
		if !nativeGemini {
			body = nil
		}
	default:
		if nativeGemini {
			body = GeminiToOpenAIError(body)
		} else if nativeAnthropic {
			body = AnthropicToOpenAIError(body)
		}
	}
	if len(body) > 0 {
		if code := providerError.Get("code"); code.Exists() && code.Type != gjson.Null {
			body, _ = sjson.SetRawBytes(body, "error.code", []byte(code.Raw))
		}
	}
	failure.ProviderBody = body
	return failure
}

func (s *protocolNoticeSink) failureBody(failure TerminalFailure) ([]byte, error) {
	if len(failure.ProviderBody) > 0 && json.Valid(failure.ProviderBody) {
		return failure.ProviderBody, nil
	}
	switch s.format {
	case FormatAnthropic:
		return json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": failure.Type, "message": failure.Message}})
	case FormatGemini:
		return json.Marshal(map[string]any{"error": map[string]any{"code": failure.Status, "status": failure.GeminiStatus, "message": failure.Message}})
	default:
		return json.Marshal(map[string]any{"error": map[string]any{"type": failure.Type, "code": failure.Code, "message": failure.Message, "param": nil}})
	}
}

// FailureSnapshot contains provider output assembled before terminal failure;
// the downstream encoder adds its own independently delivered notice items.
func (t *ResponsesWriter) FailureSnapshot() json.RawMessage {
	if !t.passthrough {
		snapshot, _ := json.Marshal(map[string]any{"output": t.assembleOutput()})
		return snapshot
	}
	indexes := make([]int, 0, len(t.failureOutput))
	for index := range t.failureOutput {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	items := make([]json.RawMessage, 0, len(indexes))
	for _, index := range indexes {
		items = append(items, t.failureOutput[index])
	}
	snapshot, _ := json.Marshal(map[string]any{"output": items})
	return snapshot
}

func (t *ResponsesWriter) failureSnapshotOutput() []json.RawMessage {
	snapshot := t.failureSnapshot
	if len(snapshot) == 0 {
		snapshot = t.FailureSnapshot()
	}
	items := append([]json.RawMessage{}, t.nativeNoticeItems...)
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		seen[gjson.GetBytes(item, "id").Str] = struct{}{}
	}
	for _, item := range gjson.GetBytes(snapshot, "output").Array() {
		if id := item.Get("id").Str; id != "" {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
		}
		items = append(items, json.RawMessage(item.Raw))
	}
	return items
}

func responsesFailurePayload(payload []byte) bool {
	root := gjson.ParseBytes(payload)
	return root.Get("error").IsObject() || root.Get("response.error").IsObject() ||
		responsesEventType(root.Get("type").Str) == responsesFailed || root.Get("type").Str == "error"
}

func (t *ResponsesWriter) captureFailureEvent(event []byte) {
	eventName, payload := sse.ParseEvent(event)
	if !gjson.ValidBytes(payload) {
		return
	}
	root := gjson.ParseBytes(payload)
	kind := responsesEventType(root.Get("type").Str)
	if kind == "" {
		kind = responsesEventType(eventName)
	}
	index := int(root.Get("output_index").Int())
	if t.failureOutput == nil {
		t.failureOutput = make(map[int]json.RawMessage)
	}
	item := []byte(t.failureOutput[index])
	switch kind {
	case responsesOutputItemAdded, responsesOutputItemDone:
		if next := root.Get("item"); next.IsObject() {
			item = []byte(next.Raw)
		}
	case responsesContentPartAdded, responsesContentPartDone, responsesReasoningSummaryPartAdded, responsesReasoningSummaryPartDone:
		if len(item) == 0 || !root.Get("part").IsObject() {
			return
		}
		path := "content." + strconv.FormatInt(root.Get("content_index").Int(), 10)
		if kind == responsesReasoningSummaryPartAdded || kind == responsesReasoningSummaryPartDone {
			path = "summary." + strconv.FormatInt(root.Get("summary_index").Int(), 10)
		}
		item, _ = sjson.SetRawBytes(item, path, []byte(root.Get("part").Raw))
	case responsesOutputTextDelta, responsesOutputTextDone, responsesRefusalDelta, responsesRefusalDone,
		responsesReasoningSummaryDelta, responsesReasoningSummaryDone, responsesReasoningTextDelta, responsesReasoningTextDone:
		item = captureFailureText(item, root, kind)
	case responsesFunctionCallArgumentsDelta, responsesCustomToolCallInputDelta, responsesFunctionCallArgumentsDone, responsesCustomToolCallInputDone:
		if len(item) == 0 {
			return
		}
		field := "arguments"
		if kind == responsesCustomToolCallInputDelta || kind == responsesCustomToolCallInputDone {
			field = "input"
		}
		value := root.Get(field).Str
		if kind == responsesFunctionCallArgumentsDelta || kind == responsesCustomToolCallInputDelta {
			value = gjson.GetBytes(item, field).Str + root.Get("delta").Str
		}
		item, _ = sjson.SetBytes(item, field, value)
	default:
		return
	}
	if len(item) > 0 {
		t.failureOutput[index] = bytes.Clone(item)
	}
}

func captureFailureText(item []byte, event gjson.Result, kind responsesEventType) []byte {
	itemType, partType := responsesMessageItem, responsesOutputText
	array, index, field := "content", event.Get("content_index").Int(), "text"
	delta := kind == responsesOutputTextDelta || kind == responsesRefusalDelta || kind == responsesReasoningSummaryDelta || kind == responsesReasoningTextDelta
	switch kind {
	case responsesRefusalDelta, responsesRefusalDone:
		partType, field = responsesRefusal, "refusal"
	case responsesReasoningSummaryDelta, responsesReasoningSummaryDone:
		itemType, partType = responsesReasoningItem, responsesSummaryText
		array, index = "summary", event.Get("summary_index").Int()
	case responsesReasoningTextDelta, responsesReasoningTextDone:
		itemType, partType = responsesReasoningItem, responsesReasoningText
	}
	if len(item) == 0 {
		envelope := map[string]any{"id": event.Get("item_id").Str, "type": itemType, "status": "in_progress", array: []any{}}
		if itemType == responsesMessageItem {
			envelope["role"] = "assistant"
		}
		item, _ = json.Marshal(envelope)
	}
	path := array + "." + strconv.FormatInt(index, 10)
	if !gjson.GetBytes(item, path).IsObject() {
		part := map[string]any{"type": partType}
		if partType == responsesOutputText {
			part["annotations"] = []any{}
		}
		encodedPart, _ := json.Marshal(part)
		item, _ = sjson.SetRawBytes(item, path, encodedPart)
	}
	text := event.Get(field).Str
	if delta {
		text = gjson.GetBytes(item, path+"."+field).Str + event.Get("delta").Str
	}
	item, _ = sjson.SetBytes(item, path+"."+field, text)
	return item
}
