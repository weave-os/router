package translate

import (
	"bufio"
	"bytes"
	"net/http"
	"strings"

	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
)

var naturalStopReasons = map[string]struct{}{
	"end_turn":      {},
	"stop_sequence": {},
}

type AnthropicRoutingFooterWriter struct {
	inner   http.ResponseWriter
	flusher http.Flusher
	bw      *bufio.Writer

	footer string

	buf     bytes.Buffer
	scanner sse.Scanner

	streaming      bool
	headersEmitted bool
	footerEmitted  bool

	pendingStop   []byte
	pendingIndex  int
	pendingIsText bool
	curType       string
}

func NewAnthropicRoutingFooterWriter(w http.ResponseWriter, footer string) *AnthropicRoutingFooterWriter {
	flusher, _ := w.(http.Flusher)
	return &AnthropicRoutingFooterWriter{
		inner:   w,
		flusher: flusher,
		bw:      bufio.NewWriterSize(w, 4096),
		footer:  footer,
	}
}

func (w *AnthropicRoutingFooterWriter) Header() http.Header {
	return w.inner.Header()
}

func (w *AnthropicRoutingFooterWriter) WriteHeader(code int) {
	if w.headersEmitted {
		return
	}
	ct := w.inner.Header().Get("Content-Type")
	w.streaming = strings.Contains(ct, "text/event-stream") && code < 400
	w.headersEmitted = true
	sse.PrepareJSONResponse(w.inner)
	w.inner.WriteHeader(code)
}

func (w *AnthropicRoutingFooterWriter) Write(data []byte) (int, error) {
	if !w.streaming || w.footer == "" {
		return sse.WriteJSONChunk(w.inner, data)
	}
	return w.processUpstream(data)
}

func (w *AnthropicRoutingFooterWriter) Flush() {
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

func (w *AnthropicRoutingFooterWriter) processUpstream(data []byte) (int, error) {
	w.buf.Write(data)
	for {
		event, n := w.scanner.Next(w.buf.Bytes())
		if n == 0 {
			break
		}
		eventType, eventData := sse.ParseEvent(event)

		switch string(eventType) {
		case "content_block_start":
			w.flushPendingStop()
			w.curType = gjson.GetBytes(eventData, "content_block.type").String()
			w.bw.Write(event[:n])

		case "content_block_delta":
			w.flushPendingStop()
			w.bw.Write(event[:n])

		case "content_block_stop":
			w.flushPendingStop()
			w.pendingStop = append([]byte(nil), event[:n]...)
			w.pendingIndex = int(gjson.GetBytes(eventData, "index").Int())
			w.pendingIsText = w.curType == "text"

		case "message_delta":
			if !w.footerEmitted && w.pendingStop != nil && w.pendingIsText && w.naturalStop(eventData) {
				w.emitFooterDelta(w.pendingIndex)
				w.footerEmitted = true
			}
			w.flushPendingStop()
			w.bw.Write(event[:n])

		case "ping":
			w.bw.Write(event[:n])

		default:
			w.flushPendingStop()
			w.bw.Write(event[:n])
		}
		w.buf.Next(n)
	}
	if err := w.bw.Flush(); err != nil {
		return 0, err
	}
	if w.flusher != nil {
		w.flusher.Flush()
	}
	return len(data), nil
}

func (w *AnthropicRoutingFooterWriter) flushPendingStop() {
	if w.pendingStop == nil {
		return
	}
	w.bw.Write(w.pendingStop)
	w.pendingStop = nil
}

func (w *AnthropicRoutingFooterWriter) naturalStop(messageDelta []byte) bool {
	_, ok := naturalStopReasons[gjson.GetBytes(messageDelta, "delta.stop_reason").String()]
	return ok
}

func (w *AnthropicRoutingFooterWriter) emitFooterDelta(index int) {
	w.bw.WriteString(`event: content_block_delta
data: {"type":"content_block_delta","index":`)
	sse.WriteJSONInt(w.bw, int64(index))
	w.bw.WriteString(`,"delta":{"type":"text_delta","text":`)
	sse.WriteJSONString(w.bw, w.footer)
	w.bw.WriteString(`}}` + "\n\n")
}

var _ http.ResponseWriter = (*AnthropicRoutingFooterWriter)(nil)
var _ http.Flusher = (*AnthropicRoutingFooterWriter)(nil)
