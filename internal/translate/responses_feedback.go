package translate

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"weave-os/router/internal/sse"
)

type responsesFeedbackTarget struct {
	itemID       string
	outputIndex  int64
	contentIndex int64
	text         string
	streamed     string
	item         json.RawMessage
	itemStarted  bool
	partStarted  bool
}

type responsesFeedbackPart struct {
	text         strings.Builder
	noticePrefix string
	insideNotice bool
	lastNewline  bool
	answerSeen   bool
}

// append classifies only new text. The retained prefix is bounded by the notice
// introducers; notice bodies and answer prefixes are never repeatedly scanned.
func (p *responsesFeedbackPart) append(text string) {
	p.text.WriteString(text)
	for _, character := range text {
		if p.answerSeen {
			return
		}
		if p.insideNotice {
			if character == '\n' && p.lastNewline {
				p.insideNotice = false
				p.lastNewline = false
			} else {
				p.lastNewline = character == '\n'
			}
			continue
		}
		if p.noticePrefix == "" && unicode.IsSpace(character) {
			continue
		}
		p.noticePrefix += string(character)
		if p.noticePrefix == routerNoticeSentinel || p.noticePrefix == legacyRoutingMarkerPrefix {
			p.noticePrefix = ""
			p.insideNotice = true
			continue
		}
		if !strings.HasPrefix(routerNoticeSentinel, p.noticePrefix) && !strings.HasPrefix(legacyRoutingMarkerPrefix, p.noticePrefix) {
			p.answerSeen = true
		}
	}
}

// ResponsesFeedbackWriter serializes a completion suffix without changing provider facts.
type ResponsesFeedbackWriter struct {
	http.ResponseWriter
	delivery      *FeedbackDelivery
	scanner       sse.Scanner
	pending       []byte
	tail          [][]byte
	target        *responsesFeedbackTarget
	items         map[int64]json.RawMessage
	startedItems  map[int64]bool
	startedParts  map[string]bool
	streamedParts map[string]*responsesFeedbackPart
	sequenceShift int64
	lastSequence  int64
	hasSequence   bool
	failure       error
}

// NewResponsesFeedbackWriter attaches feedback after Responses conversion and notices.
func NewResponsesFeedbackWriter(w http.ResponseWriter, delivery *FeedbackDelivery) *ResponsesFeedbackWriter {
	return &ResponsesFeedbackWriter{ResponseWriter: w, delivery: delivery, items: make(map[int64]json.RawMessage), startedItems: make(map[int64]bool), startedParts: make(map[string]bool), streamedParts: make(map[string]*responsesFeedbackPart)}
}

func (w *ResponsesFeedbackWriter) WriteHeader(status int) {
	if w.delivery.Enabled() {
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *ResponsesFeedbackWriter) Flush() {
	if len(w.pending) == 0 {
		if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func (w *ResponsesFeedbackWriter) Write(payload []byte) (int, error) {
	if !w.delivery.Enabled() {
		return w.ResponseWriter.Write(payload)
	}
	if w.failure != nil {
		return 0, w.failure
	}
	w.pending = append(w.pending, payload...)
	if !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		if !gjson.ValidBytes(w.pending) {
			return len(payload), nil
		}
		body := w.pending
		w.pending = nil
		if target := lastResponsesFeedbackTarget(gjson.GetBytes(body, "output")); target != nil {
			if footer := w.delivery.Take(); footer != "" {
				var err error
				body, err = sjson.SetBytes(body, "output."+strconv.FormatInt(target.outputIndex, 10)+".content."+strconv.FormatInt(target.contentIndex, 10)+".text", target.text+footer)
				if err != nil {
					w.failure = err
					return 0, err
				}
			}
		}
		_, w.failure = w.ResponseWriter.Write(body)
		return len(payload), w.failure
	}
	for {
		_, consumed := w.scanner.Next(w.pending)
		if consumed == 0 {
			break
		}
		frame := append([]byte(nil), w.pending[:consumed]...)
		w.pending = w.pending[consumed:]
		if err := w.event(frame); err != nil {
			w.failure = err
			return 0, err
		}
	}
	return len(payload), nil
}

// Finalize releases an unverified closure tail without adding feedback.
func (w *ResponsesFeedbackWriter) Finalize() (err error) {
	defer func() {
		if err != nil {
			w.failure = err
		}
	}()
	if w.failure != nil {
		return w.failure
	}
	if len(w.pending) > 0 {
		pending := w.pending
		w.pending = nil
		w.scanner.Reset()
		if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
			if err := w.event(pending); err != nil {
				return err
			}
		} else if _, err := w.ResponseWriter.Write(pending); err != nil {
			return err
		}
	}
	return w.flushTail("")
}

func responsesFeedbackPartKey(outputIndex, contentIndex int64) string {
	return strconv.FormatInt(outputIndex, 10) + ":" + strconv.FormatInt(contentIndex, 10)
}

func responsesFeedbackItemTarget(item gjson.Result, outputIndex int64) *responsesFeedbackTarget {
	if !nativeResponsesAssistantMessage(item) {
		return nil
	}
	var target *responsesFeedbackTarget
	for contentIndex, part := range item.Get("content").Array() {
		text := part.Get("text").Str
		if responsesContentType(part.Get("type").Str) != responsesOutputText || strings.TrimSpace(text) == "" || isRouterOnlyNoticeText(text) {
			continue
		}
		target = &responsesFeedbackTarget{itemID: item.Get("id").Str, outputIndex: outputIndex, contentIndex: int64(contentIndex), text: text, item: json.RawMessage(item.Raw)}
	}
	return target
}

func lastResponsesFeedbackTarget(output gjson.Result) *responsesFeedbackTarget {
	var target *responsesFeedbackTarget
	for outputIndex, item := range output.Array() {
		if next := responsesFeedbackItemTarget(item, int64(outputIndex)); next != nil {
			target = next
		}
	}
	return target
}

func (w *ResponsesFeedbackWriter) terminalTarget(output gjson.Result) *responsesFeedbackTarget {
	var target *responsesFeedbackTarget
	for outputIndex, item := range output.Array() {
		originalIndex := int64(outputIndex)
		id := item.Get("id").Str
		if id != "" {
			for index, observed := range w.items {
				if gjson.GetBytes(observed, "id").Str == id {
					originalIndex = index
					break
				}
			}
			if w.target != nil && w.target.itemID == id {
				originalIndex = w.target.outputIndex
			}
		}
		if next := responsesFeedbackItemTarget(item, originalIndex); next != nil && (target == nil || next.outputIndex >= target.outputIndex) {
			target = next
		}
	}
	return target
}

func (w *ResponsesFeedbackWriter) sameTarget(target *responsesFeedbackTarget) bool {
	return w.target != nil && w.target.outputIndex == target.outputIndex && w.target.contentIndex == target.contentIndex && (target.itemID == "" || w.target.itemID == "" || w.target.itemID == target.itemID)
}

func (w *ResponsesFeedbackWriter) selectTarget(target *responsesFeedbackTarget) error {
	if w.sameTarget(target) {
		if target.itemID != "" {
			w.target.itemID = target.itemID
		}
		if len(target.item) > 0 {
			w.target.item = target.item
		}
		return nil
	}
	if err := w.flushTail(""); err != nil {
		return err
	}
	target.itemStarted = w.startedItems[target.outputIndex]
	target.partStarted = w.startedParts[responsesFeedbackPartKey(target.outputIndex, target.contentIndex)]
	if part := w.streamedParts[responsesFeedbackPartKey(target.outputIndex, target.contentIndex)]; part != nil {
		target.streamed = part.text.String()
	}
	if len(target.item) == 0 {
		target.item = w.items[target.outputIndex]
	}
	w.target = target
	return nil
}

func (w *ResponsesFeedbackWriter) observe(root gjson.Result) error {
	eventType := responsesEventType(root.Get("type").Str)
	outputIndex := root.Get("output_index").Int()
	contentIndex := root.Get("content_index").Int()
	switch eventType {
	case responsesOutputItemAdded, responsesOutputItemDone:
		item := root.Get("item")
		if item.IsObject() {
			w.items[outputIndex] = json.RawMessage(item.Raw)
		}
		if eventType == responsesOutputItemAdded {
			w.startedItems[outputIndex] = true
		}
		if eventType == responsesOutputItemDone && nativeResponsesAssistantMessage(item) {
			for index, part := range item.Get("content").Array() {
				if responsesContentType(part.Get("type").Str) != responsesOutputText || (strings.TrimSpace(part.Get("text").Str) == "" || isRouterOnlyNoticeText(part.Get("text").Str)) {
					continue
				}
				target := &responsesFeedbackTarget{itemID: item.Get("id").Str, outputIndex: outputIndex, contentIndex: int64(index), item: json.RawMessage(item.Raw)}
				if w.target != nil && w.target.outputIndex == outputIndex && w.target.contentIndex > int64(index) {
					continue
				}
				if err := w.selectTarget(target); err != nil {
					return err
				}
				w.target.text = part.Get("text").Str
			}
		}
	case responsesContentPartAdded, responsesContentPartDone, responsesOutputTextDelta, responsesOutputTextDone:
		if (eventType == responsesContentPartAdded || eventType == responsesContentPartDone) && responsesContentType(root.Get("part.type").Str) != responsesOutputText {
			return nil
		}
		key := responsesFeedbackPartKey(outputIndex, contentIndex)
		part := w.streamedParts[key]
		if part == nil {
			part = &responsesFeedbackPart{}
			w.streamedParts[key] = part
		}
		if eventType == responsesContentPartAdded {
			w.startedParts[key] = true
			if text := root.Get("part.text").Str; text != "" {
				part = &responsesFeedbackPart{}
				part.append(text)
				w.streamedParts[key] = part
			}
		}
		text := root.Get("text").Str
		if eventType == responsesContentPartAdded || eventType == responsesContentPartDone {
			text = root.Get("part.text").Str
		}
		if eventType == responsesOutputTextDelta {
			part.append(root.Get("delta").Str)
			if !part.answerSeen {
				return nil
			}
			text = part.text.String()
		} else if strings.TrimSpace(text) == "" || isRouterOnlyNoticeText(text) {
			return nil
		}
		target := &responsesFeedbackTarget{itemID: root.Get("item_id").Str, outputIndex: outputIndex, contentIndex: contentIndex}
		if err := w.selectTarget(target); err != nil {
			return err
		}
		w.target.text = text
		w.target.streamed = part.text.String()
		w.target.partStarted = w.startedParts[key]

	}
	return nil
}

func (w *ResponsesFeedbackWriter) event(raw []byte) error {
	_, payload := sse.ParseEvent(raw)
	if !gjson.ValidBytes(payload) {
		if len(w.tail) > 0 {
			w.tail = append(w.tail, raw)
			return nil
		}
		return w.emit(raw)
	}
	root := gjson.ParseBytes(payload)
	eventType := responsesEventType(root.Get("type").Str)
	if eventType == responsesCompleted || eventType == responsesIncomplete || eventType == responsesFailed {
		if target := w.terminalTarget(root.Get("response.output")); target != nil {
			if w.target == nil || !w.sameTarget(target) {
				if err := w.selectTarget(target); err != nil {
					return err
				}
			} else {
				w.target.text = target.text
				w.target.item = target.item
			}
		}
		footer := ""
		if w.target != nil && w.target.text != "" {
			footer = w.delivery.Take()
		}
		if footer != "" {
			if err := w.prepareSuffix(footer); err != nil {
				return err
			}
			if err := w.flushTail(footer); err != nil {
				return err
			}
			rewritten, err := w.rewriteTerminal(payload, footer)
			if err != nil {
				return err
			}
			raw = replaceResponsesFeedbackPayload(raw, payload, rewritten)
		} else if err := w.flushTail(""); err != nil {
			return err
		}
		return w.emit(raw)
	}
	if err := w.observe(root); err != nil {
		return err
	}
	if len(w.tail) > 0 || (w.target != nil && w.target.text != "" && (eventType == responsesOutputTextDone || eventType == responsesContentPartDone || (eventType == responsesOutputItemDone && nativeResponsesAssistantMessage(root.Get("item"))))) {
		w.tail = append(w.tail, raw)
		return nil
	}
	return w.emit(raw)
}

func replaceResponsesFeedbackPayload(raw, oldPayload, newPayload []byte) []byte {
	offset := bytes.Index(raw, oldPayload)
	if offset < 0 {
		return raw
	}
	rewritten := make([]byte, 0, len(raw))
	rewritten = append(rewritten, raw[:offset]...)
	rewritten = append(rewritten, newPayload...)
	return append(rewritten, raw[offset+len(oldPayload):]...)
}

func (w *ResponsesFeedbackWriter) emit(raw []byte) error {
	_, payload := sse.ParseEvent(raw)
	if gjson.ValidBytes(payload) {
		sequence := gjson.GetBytes(payload, "sequence_number")
		if sequence.Type == gjson.Number {
			next := sequence.Int() + w.sequenceShift
			if w.hasSequence && next <= w.lastSequence {
				next = w.lastSequence + 1
			}
			if next != sequence.Int() {
				rewritten, err := sjson.SetBytes(payload, "sequence_number", next)
				if err != nil {
					return err
				}
				raw = replaceResponsesFeedbackPayload(raw, payload, rewritten)
			}
			w.lastSequence = next
			w.hasSequence = true
		}
	}
	_, err := w.ResponseWriter.Write(raw)
	return err
}

func (w *ResponsesFeedbackWriter) synthetic(eventType responsesEventType, fields map[string]any) error {
	fields["type"] = eventType
	next := w.lastSequence + 1
	if !w.hasSequence {
		next = 0
	}
	fields["sequence_number"] = next
	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	raw := append([]byte("event: "+string(eventType)+"\ndata: "), payload...)
	raw = append(raw, '\n', '\n')
	if _, err = w.ResponseWriter.Write(raw); err != nil {
		return err
	}
	w.lastSequence = next
	w.hasSequence = true
	w.sequenceShift++
	return nil
}

func (w *ResponsesFeedbackWriter) prepareSuffix(footer string) error {
	target := w.target
	if target.itemID == "" {
		target.itemID = newResponsesID("msg")
	}
	if target.streamed == "" && !target.itemStarted {
		item := map[string]any{"id": target.itemID, "type": responsesMessageItem, "role": EscalationRoleAssistant, "status": nativeResponsesStatusInProgress, "content": []any{}}
		if err := w.synthetic(responsesOutputItemAdded, map[string]any{"output_index": target.outputIndex, "item": item}); err != nil {
			return err
		}
		for index, part := range gjson.GetBytes(target.item, "content").Array() {
			if int64(index) >= target.contentIndex {
				break
			}
			fields := map[string]any{"item_id": target.itemID, "output_index": target.outputIndex, "content_index": index}
			fields["part"] = json.RawMessage(part.Raw)
			if err := w.synthetic(responsesContentPartAdded, fields); err != nil {
				return err
			}
			if responsesContentType(part.Get("type").Str) == responsesOutputText {
				done := map[string]any{"item_id": target.itemID, "output_index": target.outputIndex, "content_index": index, "text": part.Get("text").Str}
				if err := w.synthetic(responsesOutputTextDone, done); err != nil {
					return err
				}
			}
			if err := w.synthetic(responsesContentPartDone, fields); err != nil {
				return err
			}
		}
	}
	fields := func() map[string]any {
		return map[string]any{"item_id": target.itemID, "output_index": target.outputIndex, "content_index": target.contentIndex}
	}
	if target.streamed == "" && !target.partStarted {
		part := fields()
		content, err := w.targetPart("")
		if err != nil {
			return err
		}
		part["part"] = json.RawMessage(content)
		if err := w.synthetic(responsesContentPartAdded, part); err != nil {
			return err
		}
	}
	delta := footer
	if strings.HasPrefix(target.text, target.streamed) {
		delta = target.text[len(target.streamed):] + footer
	}
	event := fields()
	event["delta"] = delta
	return w.synthetic(responsesOutputTextDelta, event)
}

func (w *ResponsesFeedbackWriter) rewriteItem(item []byte, footer string) ([]byte, error) {
	if len(item) == 0 {
		var err error
		item, err = json.Marshal(map[string]any{"type": responsesMessageItem, "role": EscalationRoleAssistant, "content": []any{}})
		if err != nil {
			return nil, err
		}
	}
	var err error
	item, err = sjson.SetBytes(item, "id", w.target.itemID)
	if err != nil {
		return nil, err
	}
	path := "content." + strconv.FormatInt(w.target.contentIndex, 10)
	item, err = sjson.SetBytes(item, path+".type", responsesOutputText)
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(item, path+".text", w.target.text+footer)
}

func (w *ResponsesFeedbackWriter) targetPart(text string) ([]byte, error) {
	part := gjson.GetBytes(w.target.item, "content."+strconv.FormatInt(w.target.contentIndex, 10))
	if part.IsObject() {
		return sjson.SetBytes([]byte(part.Raw), "text", text)
	}
	return json.Marshal(map[string]any{"type": responsesOutputText, "text": text, "annotations": []any{}})
}

func (w *ResponsesFeedbackWriter) rewriteTerminal(payload []byte, footer string) ([]byte, error) {
	output := gjson.GetBytes(payload, "response.output")
	for index, item := range output.Array() {
		if (w.target.itemID != "" && item.Get("id").Str == w.target.itemID) || (item.Get("id").Str == "" && int64(index) == w.target.outputIndex) {
			rewritten, err := w.rewriteItem([]byte(item.Raw), footer)
			if err != nil {
				return nil, err
			}
			return sjson.SetRawBytes(payload, "response.output."+strconv.Itoa(index), rewritten)
		}
	}
	item, err := w.rewriteItem(w.target.item, footer)
	if err != nil {
		return nil, err
	}
	if !output.IsArray() {
		return sjson.SetRawBytes(payload, "response.output", append(append([]byte{'['}, item...), ']'))
	}
	return sjson.SetRawBytes(payload, "response.output.-1", item)
}

func (w *ResponsesFeedbackWriter) targetEvent(root gjson.Result, idFromItem bool) bool {
	if root.Get("output_index").Int() != w.target.outputIndex {
		return false
	}
	id := root.Get("item_id").Str
	if idFromItem {
		id = root.Get("item.id").Str
	}
	return id == "" || w.target.itemID == "" || id == w.target.itemID
}

func (w *ResponsesFeedbackWriter) flushTail(footer string) error {
	held := w.tail
	w.tail = nil
	textDone, partDone, itemDone := false, false, false
	fields := func() map[string]any {
		return map[string]any{"item_id": w.target.itemID, "output_index": w.target.outputIndex, "content_index": w.target.contentIndex}
	}
	closeText := func() error {
		if textDone {
			return nil
		}
		event := fields()
		event["text"] = w.target.text + footer
		if err := w.synthetic(responsesOutputTextDone, event); err != nil {
			return err
		}
		textDone = true
		return nil
	}
	closePart := func() error {
		if partDone {
			return nil
		}
		if err := closeText(); err != nil {
			return err
		}
		part := fields()
		content, err := w.targetPart(w.target.text + footer)
		if err != nil {
			return err
		}
		part["part"] = json.RawMessage(content)
		if err := w.synthetic(responsesContentPartDone, part); err != nil {
			return err
		}
		partDone = true
		return nil
	}
	for _, raw := range held {
		if footer != "" {
			_, payload := sse.ParseEvent(raw)
			root := gjson.ParseBytes(payload)
			var path string
			switch responsesEventType(root.Get("type").Str) {
			case responsesOutputTextDone:
				if w.targetEvent(root, false) && root.Get("content_index").Int() == w.target.contentIndex {
					path = "text"
					textDone = true
				}
			case responsesContentPartDone:
				if w.targetEvent(root, false) && root.Get("content_index").Int() == w.target.contentIndex {
					if err := closeText(); err != nil {
						return err
					}
					path = "part.text"
					partDone = true
				}
			case responsesOutputItemDone:
				if w.targetEvent(root, true) {
					if err := closePart(); err != nil {
						return err
					}
					path = "item.content." + strconv.FormatInt(w.target.contentIndex, 10) + ".text"
					itemDone = true
				}
			}
			if path != "" {
				rewritten, err := sjson.SetBytes(payload, path, w.target.text+footer)
				if err != nil {
					return err
				}
				raw = replaceResponsesFeedbackPayload(raw, payload, rewritten)
			}
		}
		if err := w.emit(raw); err != nil {
			return err
		}
	}
	if footer != "" {
		if err := closePart(); err != nil {
			return err
		}
		if !itemDone {
			item, err := w.rewriteItem(w.target.item, footer)
			if err != nil {
				return err
			}
			item, err = sjson.SetBytes(item, "status", nativeResponsesStatusCompleted)
			if err != nil {
				return err
			}
			return w.synthetic(responsesOutputItemDone, map[string]any{"output_index": w.target.outputIndex, "item": json.RawMessage(item)})
		}
	}
	return nil
}
