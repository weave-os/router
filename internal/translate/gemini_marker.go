package translate

import (
	"net/http"

	"weave-os/router/internal/sse"
)

type GeminiRoutingMarkerWriter struct {
	sse.ChunkedWriter

	marker string

	markerEmitted bool
}

func NewGeminiRoutingMarkerWriter(w http.ResponseWriter, marker string) *GeminiRoutingMarkerWriter {
	return &GeminiRoutingMarkerWriter{
		ChunkedWriter: sse.NewChunkedWriter(w, 4096),
		marker:        marker,
	}
}

func (w *GeminiRoutingMarkerWriter) Write(data []byte) (int, error) {
	if w.Streaming && !w.markerEmitted {
		w.markerEmitted = true
		if w.marker != "" {
			if err := w.emitMarkerChunk(); err != nil {
				return 0, err
			}
		}
	}
	return sse.WriteJSONChunk(w.Inner, data)
}

func (w *GeminiRoutingMarkerWriter) Prepare(streaming bool) {
	if streaming && !w.markerEmitted {
		w.Streaming = true
	}
}

func (w *GeminiRoutingMarkerWriter) Prelude(streaming bool) error {
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

func (w *GeminiRoutingMarkerWriter) emitMarkerChunk() error {
	w.BW.WriteString(`data: {"candidates":[{"content":{"parts":[{"text":`)
	sse.WriteJSONString(w.BW, w.marker)
	w.BW.WriteString(`}],"role":"model"},"index":0}]}`)
	w.BW.WriteString("\n\n")
	return w.FlushEvent()
}

var _ http.ResponseWriter = (*GeminiRoutingMarkerWriter)(nil)
var _ http.Flusher = (*GeminiRoutingMarkerWriter)(nil)
