package translate

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"weave-os/router/internal/sse"
)

type completionFinishReason string

const (
	completionChatStop          completionFinishReason = "stop"
	completionAnthropicEnd      completionFinishReason = "end_turn"
	completionAnthropicSequence completionFinishReason = "stop_sequence"
	completionGeminiStop        completionFinishReason = "STOP"
)

type completionAnthropicEvent string

const (
	completionBlockStart   completionAnthropicEvent = "content_block_start"
	completionBlockDelta   completionAnthropicEvent = "content_block_delta"
	completionMessageDelta completionAnthropicEvent = "message_delta"
	completionMessageStop  completionAnthropicEvent = "message_stop"
	completionError        completionAnthropicEvent = "error"
)

type completionAnthropicBlock string

const (
	completionTextBlock completionAnthropicBlock = "text"
	completionToolBlock completionAnthropicBlock = "tool_use"
	completionTextDelta completionAnthropicBlock = "text_delta"
)

// CompletionObserver records original provider evidence before any conversion
// can discard refusal or finish reasons. It forwards progress hooks unchanged;
// observing completion never reports progress or changes dispatch success.
type CompletionObserver struct {
	inner     http.ResponseWriter
	format    Format
	streaming bool
	delivery  *FeedbackDelivery
	pending   []byte
	scanner   sse.Scanner
	echoTail  string
}

// NewCompletionObserver starts fresh provider facts for one dispatch attempt.
func NewCompletionObserver(w http.ResponseWriter, format Format, streaming bool, delivery *FeedbackDelivery) *CompletionObserver {
	delivery.facts = CompletionFacts{}
	return &CompletionObserver{inner: w, format: format, streaming: streaming, delivery: delivery}
}

func (o *CompletionObserver) Header() http.Header { return o.inner.Header() }
func (o *CompletionObserver) WriteHeader(status int) {
	if status >= http.StatusBadRequest {
		o.delivery.facts.Failed = true
	}
	o.inner.WriteHeader(status)
}
func (o *CompletionObserver) Flush() {
	if f, ok := o.inner.(http.Flusher); ok {
		f.Flush()
	}
}
func (o *CompletionObserver) ArmOutputProgress(mark func()) bool {
	if armer, ok := o.inner.(interface{ ArmOutputProgress(func()) bool }); ok {
		return armer.ArmOutputProgress(mark)
	}
	return false
}
func (o *CompletionObserver) ArmReasoningProgress(mark func()) bool {
	if armer, ok := o.inner.(interface{ ArmReasoningProgress(func()) bool }); ok {
		return armer.ArmReasoningProgress(mark)
	}
	return false
}
func (o *CompletionObserver) Write(chunk []byte) (int, error) {
	forwarded := len(o.pending)
	o.pending = append(o.pending, chunk...)
	if !o.streaming {
		if gjson.ValidBytes(o.pending) {
			o.observe(gjson.ParseBytes(o.pending), "", true)
			o.pending = nil
		}
		return o.inner.Write(chunk)
	}
	for {
		record, n := o.scanner.Next(o.pending)
		if n == 0 {
			break
		}
		event, payload := sse.ParseEvent(record)
		if gjson.ValidBytes(payload) {
			o.observe(gjson.ParseBytes(payload), string(event), false)
		}
		if n > forwarded {
			if _, err := o.inner.Write(o.pending[forwarded:n]); err != nil {
				o.delivery.facts.Failed = true
				return 0, err
			}
		}
		forwarded = max(0, forwarded-n)
		o.pending = o.pending[n:]
	}
	// Forward incomplete records immediately too: feedback observation must not
	// move provider commitment or change byte/progress/accounting boundaries.
	if len(o.pending) > forwarded {
		if _, err := o.inner.Write(o.pending[forwarded:]); err != nil {
			o.delivery.facts.Failed = true
			return 0, err
		}
	}
	return len(chunk), nil
}

// Finish inspects a valid final record without a blank-line delimiter before
// downstream finalizers run. Its bytes have already passed through unchanged.
func (o *CompletionObserver) Finish(err error) error {
	if err != nil {
		o.delivery.facts.Failed = true
	}
	if o.streaming && len(o.pending) > 0 {
		event, payload := sse.ParseEvent(o.pending)
		if gjson.ValidBytes(payload) {
			o.observe(gjson.ParseBytes(payload), string(event), false)
		}
	}
	o.pending = nil
	return err
}

func (o *CompletionObserver) text(text string) {
	if strings.TrimSpace(text) != "" {
		o.delivery.facts.Answer = true
	}
	// Keep enough overlap for the stable sentinel even across one-byte deltas.
	tail := o.echoTail + text
	if strings.Contains(tail, "_Weave Router feedback:_") {
		o.delivery.facts.Echoed = true
	}
	if len(tail) > 64 {
		tail = strings.Clone(tail[len(tail)-64:])
	}
	o.echoTail = tail
}

// primaryCompletionElement makes choice/candidate identity explicit. An indexed
// secondary cannot qualify the primary; legacy unindexed payloads use the first.
func primaryCompletionElement(elements gjson.Result) (gjson.Result, int) {
	all := elements.Array()
	for i, element := range all {
		if index := element.Get("index"); index.Exists() && index.Int() == 0 {
			return element, i
		}
	}
	if len(all) > 0 && !all[0].Get("index").Exists() {
		return all[0], 0
	}
	return gjson.Result{}, -1
}

func (o *CompletionObserver) recordFinishReason(reason completionFinishReason, natural ...completionFinishReason) {
	o.delivery.facts.Outcome = CompletionSuppressed
	for _, allowed := range natural {
		if reason == allowed {
			o.delivery.facts.Outcome = CompletionNatural
			break
		}
	}
}
func (o *CompletionObserver) anthropicBlock(block gjson.Result) {
	switch completionAnthropicBlock(block.Get("type").Str) {
	case completionTextBlock, completionTextDelta:
		o.text(block.Get("text").Str)
	case completionToolBlock:
		o.delivery.facts.Tool = true
	}
}
func (o *CompletionObserver) responsesItem(item gjson.Result) {
	switch responsesItemType(item.Get("type").Str) {
	case responsesMessageItem:
		for _, part := range item.Get("content").Array() {
			o.responsesPart(part)
		}
	case responsesReasoningItem, responsesWebSearchCallItem:
		// Hosted search completes within the provider action; it does not hand
		// a tool step to the client. The terminal still decides natural completion.
	default:
		// New opaque output items cannot accidentally qualify as a natural answer.
		o.delivery.facts.Tool = true
	}
}
func (o *CompletionObserver) responsesPart(part gjson.Result) {
	switch responsesContentType(part.Get("type").Str) {
	case responsesOutputText:
		o.text(part.Get("text").Str)
	case responsesRefusal:
		o.delivery.facts.Refusal = true
	}
}
func (o *CompletionObserver) responsesTerminal(response gjson.Result, completedEvent bool) {
	for _, item := range response.Get("output").Array() {
		o.responsesItem(item)
	}
	o.delivery.facts.Closed = true
	status := nativeResponsesStatus(response.Get("status").Str)
	if responsesTerminalIsFailure(response) || (status != nativeResponsesStatusCompleted && !(completedEvent && status == "")) {
		o.delivery.facts.Failed = true
		o.delivery.facts.Outcome = CompletionSuppressed
	} else {
		o.delivery.facts.Outcome = CompletionNatural
	}
}
func (o *CompletionObserver) observe(root gjson.Result, event string, buffered bool) {
	facts := &o.delivery.facts
	if root.Get("error").Exists() && root.Get("error").Type != gjson.Null {
		facts.Failed = true
	}
	switch o.format {
	case FormatOpenAI:
		choice, _ := primaryCompletionElement(root.Get("choices"))
		if !choice.Exists() {
			return
		}
		message := choice.Get("delta")
		if buffered {
			message = choice.Get("message")
		}
		o.text(message.Get("content").Str)
		if message.Get("tool_calls").IsArray() && len(message.Get("tool_calls").Array()) > 0 || message.Get("function_call").Exists() {
			facts.Tool = true
		}
		if refusal := message.Get("refusal"); refusal.Exists() && refusal.Type != gjson.Null {
			facts.Refusal = true
		}
		if reason := choice.Get("finish_reason"); reason.Exists() && reason.Type != gjson.Null {
			o.recordFinishReason(completionFinishReason(reason.Str), completionChatStop)
			facts.Closed = true
		}
	case FormatGemini:
		candidate, _ := primaryCompletionElement(root.Get("candidates"))
		if !candidate.Exists() {
			return
		}
		for _, part := range candidate.Get("content.parts").Array() {
			if strings.TrimSpace(part.Get("functionCall.name").Str) != "" {
				facts.Tool = true
			}
			if !part.Get("thought").Bool() {
				o.text(part.Get("text").Str)
			}
		}
		if reason := candidate.Get("finishReason"); reason.Exists() {
			o.recordFinishReason(completionFinishReason(reason.Str), completionGeminiStop)
			facts.Closed = true
		}
	case FormatAnthropic:
		if buffered {
			for _, block := range root.Get("content").Array() {
				o.anthropicBlock(block)
			}
			o.recordFinishReason(completionFinishReason(root.Get("stop_reason").Str), completionAnthropicEnd, completionAnthropicSequence)
			facts.Closed = true
			return
		}
		if event == "" {
			event = root.Get("type").Str
		}
		switch completionAnthropicEvent(event) {
		case completionBlockStart:
			o.anthropicBlock(root.Get("content_block"))
		case completionBlockDelta:
			o.anthropicBlock(root.Get("delta"))
		case completionMessageDelta:
			if reason := root.Get("delta.stop_reason"); reason.Exists() && reason.Type != gjson.Null {
				o.recordFinishReason(completionFinishReason(reason.Str), completionAnthropicEnd, completionAnthropicSequence)
			}
		case completionMessageStop:
			facts.Closed = true
		case completionError:
			facts.Failed = true
		}
	case FormatOpenAIResponses:
		if buffered {
			o.responsesTerminal(root, false)
			return
		}
		if event == "" {
			event = root.Get("type").Str
		}
		switch responsesEventType(event) {
		case responsesFunctionCallArgumentsDelta, responsesCustomToolCallInputDelta:
			facts.Tool = true
		case responsesOutputTextDelta:
			o.text(root.Get("delta").Str)
		case responsesOutputTextDone:
			o.text(root.Get("text").Str)
		case responsesRefusalDelta, responsesRefusalDone:
			facts.Refusal = true
		case responsesContentPartAdded, responsesContentPartDone:
			o.responsesPart(root.Get("part"))
		case responsesOutputItemAdded, responsesOutputItemDone:
			o.responsesItem(root.Get("item"))
		case responsesCompleted:
			o.responsesTerminal(root.Get("response"), true)
		case responsesIncomplete, responsesFailed:
			facts.Closed = true
			facts.Failed = true
		default:
			if completionAnthropicEvent(event) == completionError {
				facts.Failed = true
			}
		}
	}
}
