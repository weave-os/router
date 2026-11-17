package translate

import (
	"net/http"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
)

type OpenAIRoutingMarkerWriter struct {
	sse.ChunkedWriter

	marker string
	model  string

	markerEmitted bool

	onOutputProgress func()
	outputLeftover   []byte
	leftoverScanner  sse.Scanner
}

func NewOpenAIRoutingMarkerWriter(w http.ResponseWriter, model, marker string) *OpenAIRoutingMarkerWriter {
	return &OpenAIRoutingMarkerWriter{
		ChunkedWriter: sse.NewChunkedWriter(w, 4096),
		marker:        marker,
		model:         model,
	}
}

func (w *OpenAIRoutingMarkerWriter) Write(data []byte) (int, error) {
	if w.Streaming && !w.markerEmitted {
		w.markerEmitted = true
		if w.marker != "" {
			if err := w.emitMarkerChunk(); err != nil {
				return 0, err
			}
		}
	}
	if w.Streaming && w.onOutputProgress != nil {
		w.scanOutputProgress(data)
	}
	return sse.WriteJSONChunk(w.Inner, data)
}

func (w *OpenAIRoutingMarkerWriter) ArmOutputProgress(mark func()) (armed bool) {
	if !w.Streaming {
		return false
	}
	w.onOutputProgress = mark
	return true
}

func (w *OpenAIRoutingMarkerWriter) scanOutputProgress(data []byte) {
	w.outputLeftover = append(w.outputLeftover, data...)
	buf := w.outputLeftover
	for {
		event, n := w.leftoverScanner.Next(buf)
		if n == 0 {
			break
		}
		_, payload := sse.ParseEvent(event)
		buf = buf[n:]
		if len(payload) == 0 {
			continue
		}
		if chunkCarriesOutput(payload) {
			w.onOutputProgress()
		}
	}
	rest := copy(w.outputLeftover, buf)
	w.outputLeftover = w.outputLeftover[:rest]
}

func chunkCarriesOutput(payload []byte) bool {
	choice := gjson.GetBytes(payload, "choices.0")
	if !choice.Exists() {
		return false
	}
	if fr := choice.Get("finish_reason"); fr.Type == gjson.String && fr.Str != "" {
		return true
	}
	delta := choice.Get("delta")
	if !delta.Exists() {
		return false
	}
	if delta.Get("content").Str != "" {
		return true
	}
	if delta.Get("reasoning_content").Str != "" || delta.Get("reasoning").Str != "" {
		return true
	}
	if delta.Get("tool_calls.#").Int() > 0 {
		return true
	}
	return false
}

func (w *OpenAIRoutingMarkerWriter) Prelude(streaming bool) error {
	if !streaming || w.markerEmitted {
		return nil
	}
	w.Inner.Header().Set("Content-Type", "text/event-stream")
	w.Streaming = true
	if !w.HeadersEmitted {
		w.HeadersEmitted = true
		w.Inner.WriteHeader(http.StatusOK)
	}
	w.markerEmitted = true
	if w.marker == "" {
		w.BW.WriteString(": routing complete\n\n")
		return w.FlushEvent()
	}
	return w.emitMarkerChunk()
}

func (w *OpenAIRoutingMarkerWriter) ContinueAfterPrelude(streaming bool) {
	if !streaming {
		return
	}
	w.Streaming = true
	w.HeadersEmitted = true
	w.markerEmitted = true
}

func (w *OpenAIRoutingMarkerWriter) emitMarkerChunk() error {
	w.BW.WriteString(`data: {"id":`)
	sse.WriteJSONString(w.BW, generateChatCmplID())
	w.BW.WriteString(`,"object":"chat.completion.chunk","created":`)
	sse.WriteJSONInt(w.BW, time.Now().Unix())
	w.BW.WriteString(`,"model":`)
	sse.WriteJSONString(w.BW, w.model)
	w.BW.WriteString(`,"choices":[{"index":0,"delta":{"role":"assistant","content":`)
	sse.WriteJSONString(w.BW, w.marker)
	w.BW.WriteString(`},"finish_reason":null}]}`)
	w.BW.WriteString("\n\n")
	return w.FlushEvent()
}

var _ http.ResponseWriter = (*OpenAIRoutingMarkerWriter)(nil)
var _ http.Flusher = (*OpenAIRoutingMarkerWriter)(nil)
var _ providers.OutputProgressArmer = (*OpenAIRoutingMarkerWriter)(nil)
