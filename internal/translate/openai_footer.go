package translate

import (
	"bufio"
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"time"

	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAIRoutingFooterWriter inserts authorized feedback before a downstream
// Chat finish chunk, splitting coalesced answer text to preserve ordering.
type OpenAIRoutingFooterWriter struct {
	inner   http.ResponseWriter
	flusher http.Flusher
	bw      *bufio.Writer

	delivery *FeedbackDelivery

	buf     bytes.Buffer
	scanner sse.Scanner

	streaming      bool
	headersEmitted bool
}

// NewOpenAIRoutingFooterWriter creates a downstream Chat insertion adapter.
func NewOpenAIRoutingFooterWriter(w http.ResponseWriter, delivery *FeedbackDelivery) *OpenAIRoutingFooterWriter {
	flusher, _ := w.(http.Flusher)
	return &OpenAIRoutingFooterWriter{
		inner:    w,
		flusher:  flusher,
		bw:       bufio.NewWriterSize(w, 4096),
		delivery: delivery,
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
	w.inner.WriteHeader(code)
}

func (w *OpenAIRoutingFooterWriter) Write(data []byte) (int, error) {
	if !w.streaming || !w.delivery.Enabled() {
		return w.inner.Write(data)
	}
	return w.processUpstream(data)
}

// Flush implements http.Flusher.
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

		choice, position := primaryCompletionElement(gjson.GetBytes(payload, "choices"))
		if position >= 0 && choice.Get("finish_reason").Str != "" {
			if footer := w.delivery.Take(); footer != "" {
				if choice.Get("delta.content").Str != "" || choice.Get("delta.content").IsArray() {
					if err := w.emitCoalescedWithFooter(payload, position, footer); err != nil {
						return 0, err
					}
					w.buf.Next(n)
					continue
				}
				w.emitFooterChunk(payload, footer)
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

// emitCoalescedWithFooter retains other choices on the original content frame
// only, so splitting one choice cannot replay another choice's content or usage.
func (w *OpenAIRoutingFooterWriter) emitCoalescedWithFooter(finishChunk []byte, position int, footer string) error {
	path := "choices." + strconv.Itoa(position)
	contentChunk, err := sjson.SetRawBytes(finishChunk, path+".finish_reason", []byte("null"))
	if err != nil {
		return err
	}
	choice := gjson.GetBytes(finishChunk, path)
	finishOnly, err := sjson.SetRawBytes(finishChunk, "choices", []byte("["+choice.Raw+"]"))
	if err != nil {
		return err
	}
	finishOnly, err = sjson.SetRawBytes(finishOnly, "choices.0.delta", []byte("{}"))
	if err != nil {
		return err
	}
	contentChunk, err = sjson.DeleteBytes(contentChunk, "usage")
	if err != nil {
		return err
	}
	w.writeData(contentChunk)
	w.emitFooterChunk(finishChunk, footer)
	w.writeData(finishOnly)
	return nil
}

// writeData frames a JSON payload as a single SSE data event.
func (w *OpenAIRoutingFooterWriter) writeData(payload []byte) {
	w.bw.WriteString("data: ")
	w.bw.Write(payload)
	w.bw.WriteString("\n\n")
}

// emitFooterChunk writes a chat.completion.chunk carrying the footer as
// assistant delta content. It reuses the id/model from the finish chunk so the
// injected chunk is indistinguishable from upstream framing.
func (w *OpenAIRoutingFooterWriter) emitFooterChunk(finishChunk []byte, footer string) {
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
	sse.WriteJSONString(w.bw, footer)
	w.bw.WriteString(`},"finish_reason":null}]}`)
	w.bw.WriteString("\n\n")
}

// Finalize processes an explicit final terminal or drains incomplete framing unchanged.
func (w *OpenAIRoutingFooterWriter) Finalize() error {
	// A valid terminal may omit its blank-line delimiter at EOF.
	_, payload := sse.ParseEvent(w.buf.Bytes())
	choice, _ := primaryCompletionElement(gjson.GetBytes(payload, "choices"))
	if gjson.ValidBytes(payload) && choice.Get("finish_reason").Str != "" {
		if _, err := w.Write([]byte("\n\n")); err != nil {
			return err
		}
	}

	w.bw.Write(w.buf.Bytes())
	w.buf.Reset()
	w.scanner.Reset()
	return w.bw.Flush()
}

var _ http.ResponseWriter = (*OpenAIRoutingFooterWriter)(nil)
var _ http.Flusher = (*OpenAIRoutingFooterWriter)(nil)
