package translate

import (
	"bufio"
	"bytes"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type OpenAIRoutingFooterWriter struct {
	inner   http.ResponseWriter
	flusher http.Flusher
	bw      *bufio.Writer

	footer string

	buf     bytes.Buffer
	scanner sse.Scanner

	streaming      bool
	headersEmitted bool
	footerEmitted  bool
	sawToolCall    bool
}

func NewOpenAIRoutingFooterWriter(w http.ResponseWriter, footer string) *OpenAIRoutingFooterWriter {
	flusher, _ := w.(http.Flusher)
	return &OpenAIRoutingFooterWriter{
		inner:   w,
		flusher: flusher,
		bw:      bufio.NewWriterSize(w, 4096),
		footer:  footer,
	}
}

func (w *OpenAIRoutingFooterWriter) Header() http.Header {
	return w.inner.Header()
}

func (w *OpenAIRoutingFooterWriter) WriteHeader(code int) {
	if w.headersEmitted {
		return
	}
	ct := w.inner.Header().Get("Content-Type")
	w.streaming = strings.Contains(ct, "text/event-stream") && code < 400
	w.headersEmitted = true
	sse.PrepareJSONResponse(w.inner)
	w.inner.WriteHeader(code)
}

func (w *OpenAIRoutingFooterWriter) Write(data []byte) (int, error) {
	if !w.streaming || w.footer == "" {
		return sse.WriteJSONChunk(w.inner, data)
	}
	return w.processUpstream(data)
}

func (w *OpenAIRoutingFooterWriter) Flush() {
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

func (w *OpenAIRoutingFooterWriter) processUpstream(data []byte) (int, error) {
	w.buf.Write(data)
	for {
		event, n := w.scanner.Next(w.buf.Bytes())
		if n == 0 {
			break
		}
		_, payload := sse.ParseEvent(event)

		if chunkHasToolCall(payload) {
			w.sawToolCall = true
		}
		if !w.footerEmitted && w.shouldInject(payload) {
			w.footerEmitted = true
			if gjson.GetBytes(payload, "choices.0.delta.content").String() != "" {
				w.emitCoalescedWithFooter(payload)
				w.buf.Next(n)
				continue
			}
			w.emitFooterChunk(payload)
		}
		w.bw.Write(event[:n])
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

func (w *OpenAIRoutingFooterWriter) shouldInject(payload []byte) bool {
	if w.sawToolCall {
		return false
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return false
	}
	return gjson.GetBytes(payload, "choices.0.finish_reason").String() == "stop"
}

func chunkHasToolCall(payload []byte) bool {
	return gjson.GetBytes(payload, "choices.0.delta.tool_calls.#").Int() > 0
}

func (w *OpenAIRoutingFooterWriter) emitCoalescedWithFooter(finishChunk []byte) {
	contentChunk, err := sjson.SetRawBytes(append([]byte(nil), finishChunk...), "choices.0.finish_reason", []byte("null"))
	if err != nil {
		w.writeData(finishChunk)
		w.emitFooterChunk(finishChunk)
		return
	}
	finishOnly, err := sjson.SetRawBytes(append([]byte(nil), finishChunk...), "choices.0.delta", []byte("{}"))
	if err != nil {
		w.writeData(finishChunk)
		w.emitFooterChunk(finishChunk)
		return
	}
	w.writeData(contentChunk)
	w.emitFooterChunk(finishChunk)
	w.writeData(finishOnly)
}

func (w *OpenAIRoutingFooterWriter) writeData(payload []byte) {
	w.bw.WriteString("data: ")
	w.bw.Write(payload)
	w.bw.WriteString("\n\n")
}

func (w *OpenAIRoutingFooterWriter) emitFooterChunk(finishChunk []byte) {
	id := gjson.GetBytes(finishChunk, "id").String()
	if id == "" {
		id = generateChatCmplID()
	}
	model := gjson.GetBytes(finishChunk, "model").String()

	w.bw.WriteString(`data: {"id":`)
	sse.WriteJSONString(w.bw, id)
	w.bw.WriteString(`,"object":"chat.completion.chunk","created":`)
	sse.WriteJSONInt(w.bw, time.Now().Unix())
	w.bw.WriteString(`,"model":`)
	sse.WriteJSONString(w.bw, model)
	w.bw.WriteString(`,"choices":[{"index":0,"delta":{"content":`)
	sse.WriteJSONString(w.bw, w.footer)
	w.bw.WriteString(`},"finish_reason":null}]}`)
	w.bw.WriteString("\n\n")
}

var _ http.ResponseWriter = (*OpenAIRoutingFooterWriter)(nil)
var _ http.Flusher = (*OpenAIRoutingFooterWriter)(nil)
