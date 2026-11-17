package translate

import (
	"bufio"
	"bytes"
	"net/http"
	"strings"

	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type GeminiRoutingFooterWriter struct {
	inner   http.ResponseWriter
	flusher http.Flusher
	bw      *bufio.Writer

	footer string

	buf     bytes.Buffer
	scanner sse.Scanner

	streaming       bool
	headersEmitted  bool
	footerEmitted   bool
	sawFunctionCall bool
}

func NewGeminiRoutingFooterWriter(w http.ResponseWriter, footer string) *GeminiRoutingFooterWriter {
	flusher, _ := w.(http.Flusher)
	return &GeminiRoutingFooterWriter{
		inner:   w,
		flusher: flusher,
		bw:      bufio.NewWriterSize(w, 4096),
		footer:  footer,
	}
}

func (w *GeminiRoutingFooterWriter) Header() http.Header {
	return w.inner.Header()
}

func (w *GeminiRoutingFooterWriter) WriteHeader(code int) {
	if w.headersEmitted {
		return
	}
	ct := w.inner.Header().Get("Content-Type")
	w.streaming = strings.Contains(ct, "text/event-stream") && code < 400
	w.headersEmitted = true
	sse.PrepareJSONResponse(w.inner)
	w.inner.WriteHeader(code)
}

func (w *GeminiRoutingFooterWriter) Write(data []byte) (int, error) {
	if !w.streaming || w.footer == "" {
		return sse.WriteJSONChunk(w.inner, data)
	}
	return w.processUpstream(data)
}

func (w *GeminiRoutingFooterWriter) Flush() {
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

func (w *GeminiRoutingFooterWriter) processUpstream(data []byte) (int, error) {
	w.buf.Write(data)
	for {
		event, n := w.scanner.Next(w.buf.Bytes())
		if n == 0 {
			break
		}
		_, payload := sse.ParseEvent(event)

		if chunkHasFunctionCall(payload) {
			w.sawFunctionCall = true
		}
		if !w.footerEmitted && w.shouldInject(payload) {
			w.footerEmitted = true
			if geminiChunkHasText(payload) {
				w.emitCoalescedWithFooter(payload)
				w.buf.Next(n)
				continue
			}
			w.emitFooterChunk()
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

func (w *GeminiRoutingFooterWriter) shouldInject(payload []byte) bool {
	if w.sawFunctionCall {
		return false
	}
	return gjson.GetBytes(payload, "candidates.0.finishReason").String() == "STOP"
}

func (w *GeminiRoutingFooterWriter) emitFooterChunk() {
	w.bw.WriteString(`data: {"candidates":[{"content":{"parts":[{"text":`)
	sse.WriteJSONString(w.bw, w.footer)
	w.bw.WriteString(`}],"role":"model"},"index":0}]}`)
	w.bw.WriteString("\n\n")
}

func geminiChunkHasText(payload []byte) bool {
	found := false
	gjson.GetBytes(payload, "candidates.0.content.parts").ForEach(func(_, part gjson.Result) bool {
		if part.Get("text").Exists() {
			found = true
			return false
		}
		return true
	})
	return found
}

func (w *GeminiRoutingFooterWriter) emitCoalescedWithFooter(finishChunk []byte) {
	contentChunk, err := sjson.DeleteBytes(append([]byte(nil), finishChunk...), "candidates.0.finishReason")
	if err != nil {
		w.writeData(finishChunk)
		w.emitFooterChunk()
		return
	}
	finishOnly, err := sjson.DeleteBytes(append([]byte(nil), finishChunk...), "candidates.0.content")
	if err != nil {
		w.writeData(finishChunk)
		w.emitFooterChunk()
		return
	}
	w.writeData(contentChunk)
	w.emitFooterChunk()
	w.writeData(finishOnly)
}

func (w *GeminiRoutingFooterWriter) writeData(payload []byte) {
	w.bw.WriteString("data: ")
	w.bw.Write(payload)
	w.bw.WriteString("\n\n")
}

func chunkHasFunctionCall(payload []byte) bool {
	found := false
	gjson.GetBytes(payload, "candidates.0.content.parts").ForEach(func(_, part gjson.Result) bool {
		if part.Get("functionCall.name").String() != "" {
			found = true
			return false
		}
		return true
	})
	return found
}

var _ http.ResponseWriter = (*GeminiRoutingFooterWriter)(nil)
var _ http.Flusher = (*GeminiRoutingFooterWriter)(nil)
