package translate

import (
	"bufio"
	"bytes"
	"net/http"
	"strconv"
	"strings"

	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// GeminiRoutingFooterWriter inserts authorized feedback before a downstream
// Gemini terminal, keeping any coalesced answer text ahead of the footer.
type GeminiRoutingFooterWriter struct {
	inner   http.ResponseWriter
	flusher http.Flusher
	bw      *bufio.Writer

	delivery *FeedbackDelivery

	buf     bytes.Buffer
	scanner sse.Scanner

	streaming      bool
	headersEmitted bool
}

// NewGeminiRoutingFooterWriter creates a downstream Gemini insertion adapter.
func NewGeminiRoutingFooterWriter(w http.ResponseWriter, delivery *FeedbackDelivery) *GeminiRoutingFooterWriter {
	flusher, _ := w.(http.Flusher)
	return &GeminiRoutingFooterWriter{
		inner:    w,
		flusher:  flusher,
		bw:       bufio.NewWriterSize(w, 4096),
		delivery: delivery,
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
	w.inner.WriteHeader(code)
}

func (w *GeminiRoutingFooterWriter) Write(data []byte) (int, error) {
	if !w.streaming || !w.delivery.Enabled() {
		return w.inner.Write(data)
	}
	return w.processUpstream(data)
}

// Flush implements http.Flusher.
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

		candidate, position := primaryCompletionElement(gjson.GetBytes(payload, "candidates"))
		if position >= 0 && candidate.Get("finishReason").Str != "" {
			if footer := w.delivery.Take(); footer != "" {
				if candidate.Get("content.parts.#").Int() > 0 {
					if err := w.emitCoalescedWithFooter(payload, position, footer); err != nil {
						return 0, err
					}
					w.buf.Next(n)
					continue
				}
				w.emitFooterChunk(footer)
			}
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

// emitFooterChunk writes a Gemini candidate carrying the footer as a model text
// part (no finishReason — it's a content chunk).
func (w *GeminiRoutingFooterWriter) emitFooterChunk(footer string) {
	w.bw.WriteString(`data: {"candidates":[{"content":{"parts":[{"text":`)
	sse.WriteJSONString(w.bw, footer)
	w.bw.WriteString(`}],"role":"model"},"index":0}]}`)
	w.bw.WriteString("\n\n")
}

func (w *GeminiRoutingFooterWriter) emitCoalescedWithFooter(finishChunk []byte, position int, footer string) error {
	path := "candidates." + strconv.Itoa(position)
	contentChunk, err := sjson.DeleteBytes(finishChunk, path+".finishReason")
	if err != nil {
		return err
	}
	candidate := gjson.GetBytes(finishChunk, path)
	finishOnly, err := sjson.SetRawBytes(finishChunk, "candidates", []byte("["+candidate.Raw+"]"))
	if err != nil {
		return err
	}
	finishOnly, err = sjson.DeleteBytes(finishOnly, "candidates.0.content")
	if err != nil {
		return err
	}
	contentChunk, err = sjson.DeleteBytes(contentChunk, "usageMetadata")
	if err != nil {
		return err
	}
	w.writeData(contentChunk)
	w.emitFooterChunk(footer)
	w.writeData(finishOnly)
	return nil
}

// writeData frames a JSON payload as a single SSE data event.
func (w *GeminiRoutingFooterWriter) writeData(payload []byte) {
	w.bw.WriteString("data: ")
	w.bw.Write(payload)
	w.bw.WriteString("\n\n")
}

// Finalize processes an explicit final terminal or drains incomplete framing unchanged.
func (w *GeminiRoutingFooterWriter) Finalize() error {
	// A valid terminal may omit its blank-line delimiter at EOF.
	_, payload := sse.ParseEvent(w.buf.Bytes())
	candidate, _ := primaryCompletionElement(gjson.GetBytes(payload, "candidates"))
	if gjson.ValidBytes(payload) && candidate.Get("finishReason").Str != "" {
		if _, err := w.Write([]byte("\n\n")); err != nil {
			return err
		}
	}

	w.bw.Write(w.buf.Bytes())
	w.buf.Reset()
	w.scanner.Reset()
	return w.bw.Flush()
}

var _ http.ResponseWriter = (*GeminiRoutingFooterWriter)(nil)
var _ http.Flusher = (*GeminiRoutingFooterWriter)(nil)
