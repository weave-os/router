package translate

import (
	"bufio"
	"bytes"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"weave-os/router/internal/sse"
)

const completionBlockStop completionAnthropicEvent = "content_block_stop"

// AnthropicRoutingFooterWriter folds authorized feedback into the final answer
// block. Holding its close until message_stop preserves clients that render only
// the last content block and prevents a missing terminal from receiving feedback.
type AnthropicRoutingFooterWriter struct {
	inner             http.ResponseWriter
	flusher           http.Flusher
	bw                *bufio.Writer
	delivery          *FeedbackDelivery
	buf               bytes.Buffer
	scanner           sse.Scanner
	streaming         bool
	headersEmitted    bool
	pending           bytes.Buffer
	pendingIndex      int
	pendingText       bool
	currentText       bool
	currentNoticeOnly bool
	currentAnswerSeen bool
}

// NewAnthropicRoutingFooterWriter leaves non-streaming replies and disabled delivery byte-identical.
func NewAnthropicRoutingFooterWriter(w http.ResponseWriter, delivery *FeedbackDelivery) *AnthropicRoutingFooterWriter {
	flusher, _ := w.(http.Flusher)
	return &AnthropicRoutingFooterWriter{inner: w, flusher: flusher, bw: bufio.NewWriterSize(w, 4096), delivery: delivery}
}

func (w *AnthropicRoutingFooterWriter) Header() http.Header { return w.inner.Header() }

func (w *AnthropicRoutingFooterWriter) WriteHeader(code int) {
	if w.headersEmitted {
		return
	}
	w.streaming = strings.Contains(w.inner.Header().Get("Content-Type"), "text/event-stream") && code < 400
	w.headersEmitted = true
	w.inner.WriteHeader(code)
}

func (w *AnthropicRoutingFooterWriter) Write(payload []byte) (int, error) {
	if !w.streaming || !w.delivery.Enabled() {
		return w.inner.Write(payload)
	}
	w.buf.Write(payload)
	for {
		event, consumed := w.scanner.Next(w.buf.Bytes())
		if consumed == 0 {
			break
		}
		eventType, eventPayload := sse.ParseEvent(event)
		kind := completionAnthropicEvent(eventType)
		if kind == "" {
			kind = completionAnthropicEvent(gjson.GetBytes(eventPayload, "type").Str)
		}
		switch kind {
		case completionBlockStart:
			w.currentText = completionAnthropicBlock(gjson.GetBytes(eventPayload, "content_block.type").Str) == completionTextBlock
			w.currentNoticeOnly = false
			w.currentAnswerSeen = false
			w.observeText(gjson.GetBytes(eventPayload, "content_block.text").Str)
			if w.currentText && w.currentAnswerSeen {
				w.flushPending()
			}
			w.writeTail(event[:consumed])
		case completionBlockDelta:
			if w.currentText {
				w.observeText(gjson.GetBytes(eventPayload, "delta.text").Str)
				if w.currentAnswerSeen {
					w.flushPending()
				}
			}
			w.writeTail(event[:consumed])
		case completionBlockStop:
			if w.currentText && w.currentAnswerSeen && !w.currentNoticeOnly {
				w.flushPending()
				w.pendingIndex = int(gjson.GetBytes(eventPayload, "index").Int())
				w.pendingText = true
			}
			w.writeTail(event[:consumed])
		case completionMessageStop:
			if w.pending.Len() > 0 && w.pendingText {
				if footer := w.delivery.Take(); footer != "" {
					w.emitFooterDelta(w.pendingIndex, footer)
				}
			}
			w.flushPending()
			w.bw.Write(event[:consumed])
		case completionError:
			w.flushPending()
			w.bw.Write(event[:consumed])
		default:
			w.writeTail(event[:consumed])
		}
		w.buf.Next(consumed)
	}
	if err := w.bw.Flush(); err != nil {
		return 0, err
	}
	w.Flush()
	return len(payload), nil
}

func (w *AnthropicRoutingFooterWriter) Flush() {
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

func (w *AnthropicRoutingFooterWriter) observeText(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if !w.currentAnswerSeen && isRouterOnlyNoticeText(text) {
		w.currentNoticeOnly = true
		return
	}
	w.currentAnswerSeen = true
	w.currentNoticeOnly = false
}

// writeTail holds every record after the candidate text closes, including
// trailing reasoning, until a later text block replaces it or the message ends.
func (w *AnthropicRoutingFooterWriter) writeTail(frame []byte) {
	if w.pendingText {
		w.pending.Write(frame)
	} else {
		w.bw.Write(frame)
	}
}

func (w *AnthropicRoutingFooterWriter) flushPending() {
	w.bw.Write(w.pending.Bytes())
	w.pending.Reset()
	w.pendingText = false
}

// Finalize processes an explicit final terminal or drains an unfinished tail unchanged.
func (w *AnthropicRoutingFooterWriter) Finalize() error {
	// A valid terminal may omit its blank-line delimiter at EOF.
	event, payload := sse.ParseEvent(w.buf.Bytes())
	terminal := completionAnthropicEvent(event) == completionMessageStop || completionAnthropicEvent(gjson.GetBytes(payload, "type").Str) == completionMessageStop
	if terminal && gjson.ValidBytes(payload) {
		if _, err := w.Write([]byte("\n\n")); err != nil {
			return err
		}
	}

	w.flushPending()
	w.bw.Write(w.buf.Bytes())
	w.buf.Reset()
	w.scanner.Reset()
	return w.bw.Flush()
}

func (w *AnthropicRoutingFooterWriter) emitFooterDelta(index int, footer string) {
	w.bw.WriteString(`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":`)
	sse.WriteJSONInt(w.bw, int64(index))
	w.bw.WriteString(`,"delta":{"type":"text_delta","text":`)
	sse.WriteJSONString(w.bw, footer)
	w.bw.WriteString(`}}` + "\n\n")
}

var _ http.ResponseWriter = (*AnthropicRoutingFooterWriter)(nil)
var _ http.Flusher = (*AnthropicRoutingFooterWriter)(nil)
