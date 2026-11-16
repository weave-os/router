package translate

import (
	"bytes"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type AnthropicRoutingMarkerWriter struct {
	sse.ChunkedWriter

	marker string
	model  string

	buf     bytes.Buffer
	scanner sse.Scanner

	markerEmitted bool
	indexOffset   int64

	upstreamUsage []usageField

	onOutputProgress func()
}

type usageField struct {
	key string
	raw string
}

func NewAnthropicRoutingMarkerWriter(w http.ResponseWriter, model, marker string) *AnthropicRoutingMarkerWriter {
	return &AnthropicRoutingMarkerWriter{
		ChunkedWriter: sse.NewChunkedWriter(w, 4096),
		marker:        marker,
		model:         model,
	}
}

func (w *AnthropicRoutingMarkerWriter) Write(data []byte) (int, error) {
	if w.Streaming && !w.markerEmitted {
		w.markerEmitted = true
		if w.marker != "" {
			w.indexOffset = 1
			if err := w.emitPreludeEvents(); err != nil {
				return 0, err
			}
		}
	}
	if !w.Streaming || w.indexOffset == 0 {
		return sse.WriteJSONChunk(w.Inner, data)
	}
	n, err := w.processUpstream(data)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (w *AnthropicRoutingMarkerWriter) Prelude(streaming bool) error {
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
	w.indexOffset = 1
	return w.emitPreludeEvents()
}

func (w *AnthropicRoutingMarkerWriter) ContinueAfterPrelude(streaming bool, existingBlocks int64) error {
	if !streaming || w.markerEmitted {
		return nil
	}
	w.Inner.Header().Set("Content-Type", "text/event-stream")
	w.Streaming = true
	w.HeadersEmitted = true
	w.markerEmitted = true
	w.indexOffset = existingBlocks
	if w.marker == "" {
		return nil
	}
	w.BW.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":")
	sse.WriteJSONInt(w.BW, existingBlocks)
	w.BW.WriteString(",\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	w.BW.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":")
	sse.WriteJSONInt(w.BW, existingBlocks)
	w.BW.WriteString(",\"delta\":{\"type\":\"text_delta\",\"text\":")
	sse.WriteJSONString(w.BW, w.marker)
	w.BW.WriteString("}}\n\n")
	w.BW.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":")
	sse.WriteJSONInt(w.BW, existingBlocks)
	w.BW.WriteString("}\n\n")
	w.indexOffset = existingBlocks + 1
	return w.FlushEvent()
}

func (w *AnthropicRoutingMarkerWriter) emitPreludeEvents() error {
	w.BW.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":")
	sse.WriteJSONString(w.BW, generateAnthropicMsgID())
	w.BW.WriteString(",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":")
	sse.WriteJSONString(w.BW, w.model)
	w.BW.WriteString(",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n")

	w.BW.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")

	w.BW.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":")
	sse.WriteJSONString(w.BW, w.marker)
	w.BW.WriteString("}}\n\n")

	w.BW.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")

	return w.FlushEvent()
}

func (w *AnthropicRoutingMarkerWriter) processUpstream(data []byte) (int, error) {
	w.buf.Write(data)
	for {
		event, n := w.scanner.Next(w.buf.Bytes())
		if n == 0 {
			break
		}
		eventType, eventData := sse.ParseEvent(event)

		if len(eventType) == 0 && len(eventData) == 0 {
			if _, err := w.Inner.Write(event[:n]); err != nil {
				return 0, err
			}
			w.buf.Next(n)
			continue
		}

		switch string(eventType) {
		case "message_start":
			w.captureUpstreamUsage(eventData)
			w.buf.Next(n)
			continue

		case "message_delta":
			if _, err := w.Inner.Write(w.mergeUpstreamUsage(event[:n], eventData)); err != nil {
				return 0, err
			}
			w.buf.Next(n)

		case "content_block_start", "content_block_delta", "content_block_stop":
			if string(eventType) == "content_block_delta" && w.onOutputProgress != nil {
				w.onOutputProgress()
			}
			currentIdx := gjson.GetBytes(eventData, "index").Int()
			rewritten, err := sjson.SetBytes(eventData, "index", currentIdx+w.indexOffset)
			if err != nil {
				if _, err := w.Inner.Write(event[:n]); err != nil {
					return 0, err
				}
				w.buf.Next(n)
				continue
			}
			w.BW.WriteString("event: ")
			w.BW.Write(eventType)
			w.BW.WriteString("\ndata: ")
			w.BW.Write(rewritten)
			w.BW.WriteString("\n\n")
			if err := w.BW.Flush(); err != nil {
				return 0, err
			}
			w.buf.Next(n)

		default:
			if _, err := w.Inner.Write(event[:n]); err != nil {
				return 0, err
			}
			w.buf.Next(n)
		}
	}
	return len(data), nil
}

func (w *AnthropicRoutingMarkerWriter) captureUpstreamUsage(eventData []byte) {
	usage := gjson.GetBytes(eventData, "message.usage")
	if !usage.Exists() {
		return
	}
	w.upstreamUsage = w.upstreamUsage[:0]
	usage.ForEach(func(key, value gjson.Result) bool {
		if key.String() == "output_tokens" {
			return true
		}
		w.upstreamUsage = append(w.upstreamUsage, usageField{key: key.String(), raw: value.Raw})
		return true
	})
}

func (w *AnthropicRoutingMarkerWriter) mergeUpstreamUsage(event, eventData []byte) []byte {
	if len(w.upstreamUsage) == 0 {
		return event
	}
	merged := eventData
	for _, f := range w.upstreamUsage {
		if gjson.GetBytes(merged, "usage."+f.key).Exists() {
			continue
		}
		out, err := sjson.SetRawBytes(merged, "usage."+f.key, []byte(f.raw))
		if err != nil {
			return event
		}
		merged = out
	}
	if bytes.Equal(merged, eventData) {
		return event
	}
	var b bytes.Buffer
	b.Grow(len(merged) + 32)
	b.WriteString("event: message_delta\ndata: ")
	b.Write(merged)
	b.WriteString("\n\n")
	return b.Bytes()
}

func (w *AnthropicRoutingMarkerWriter) ArmOutputProgress(mark func()) (armed bool) {
	if !w.Streaming || w.marker == "" {
		return false
	}
	w.onOutputProgress = mark
	return true
}

var _ http.ResponseWriter = (*AnthropicRoutingMarkerWriter)(nil)
var _ http.Flusher = (*AnthropicRoutingMarkerWriter)(nil)
var _ providers.OutputProgressArmer = (*AnthropicRoutingMarkerWriter)(nil)
