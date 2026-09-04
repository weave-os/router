package translate

import (
	"bytes"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AnthropicRoutingMarkerWriter injects a routing-marker text block at index 0
// of an Anthropic SSE stream, shifting upstream content_block_* indices by +1.
type AnthropicRoutingMarkerWriter struct {
	sse.ChunkedWriter

	marker string
	model  string

	buf bytes.Buffer

	markerEmitted bool

	// upstreamUsage holds the input-side token counts lifted off the upstream's
	// dropped message_start, pending merge into message_delta.
	upstreamUsage []usageField

	onOutputProgress func()
}

// usageField is one key/raw-JSON pair from an upstream usage object.
type usageField struct {
	key string
	raw string
}

// NewAnthropicRoutingMarkerWriter injects marker as a text block at index 0.
// If marker is empty, writes pass through unchanged.
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
			if err := w.emitPreludeEvents(); err != nil {
				return 0, err
			}
		}
	}
	if !w.Streaming || w.marker == "" {
		// Non-streaming or empty marker: fully transparent passthrough.
		return w.Inner.Write(data)
	}
	// Streaming with a configured marker: parse upstream SSE, drop message_start,
	// shift content_block_* indices, pass everything else through.
	n, err := w.processUpstream(data)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Prelude commits headers and emits the marker before upstream responds, so
// first-byte latency is ~routing time instead of upstream prefill+decode. Call
// once right after the routing decision; later Write/WriteHeader calls are
// idempotent. No-op if streaming is false or marker is empty.
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
	return w.emitPreludeEvents()
}

// emitPreludeEvents writes message_start followed by the routing marker as a
// text content block at index 0.
func (w *AnthropicRoutingMarkerWriter) emitPreludeEvents() error {
	// message_start — mirrors the envelope shape from AnthropicSSETranslator.
	w.BW.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":")
	sse.WriteJSONString(w.BW, generateAnthropicMsgID())
	w.BW.WriteString(",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":")
	sse.WriteJSONString(w.BW, w.model)
	w.BW.WriteString(",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n")

	// content_block_start at index 0 (text block).
	w.BW.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")

	// content_block_delta (text_delta) at index 0.
	w.BW.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":")
	sse.WriteJSONString(w.BW, w.marker)
	w.BW.WriteString("}}\n\n")

	// content_block_stop at index 0.
	w.BW.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")

	return w.FlushEvent()
}

// processUpstream parses upstream SSE events, drops message_start, and shifts
// content_block_* indices by +1; other events pass through unchanged.
func (w *AnthropicRoutingMarkerWriter) processUpstream(data []byte) (int, error) {
	// Buffer across Write calls so an SSE event split mid-stream isn't parsed
	// as truncated (and silently dropped) before its terminating blank line.
	w.buf.Write(data)
	for {
		event, n := sse.SplitNext(w.buf.Bytes())
		if n == 0 {
			break
		}
		eventType, eventData := sse.ParseEvent(event)

		if len(eventType) == 0 && len(eventData) == 0 {
			// Comment or empty — pass through as-is.
			if _, err := w.Inner.Write(event[:n]); err != nil {
				return 0, err
			}
			w.buf.Next(n)
			continue
		}

		switch string(eventType) {
		case "message_start":
			// Drop upstream's message_start; we already sent one. Ours was
			// emitted before the upstream replied, so it could not carry real
			// token counts — stash them and fold them into message_delta below.
			w.captureUpstreamUsage(eventData)
			w.buf.Next(n)
			continue

		case "message_delta":
			if _, err := w.Inner.Write(w.mergeUpstreamUsage(event[:n], eventData)); err != nil {
				return 0, err
			}
			w.buf.Next(n)

		case "content_block_start", "content_block_delta", "content_block_stop":
			// Mark on output-bearing content_block_delta only; start/stop are structural.
			if string(eventType) == "content_block_delta" && w.onOutputProgress != nil {
				w.onOutputProgress()
			}
			// Rewrite the index field: shift by +1.
			currentIdx := gjson.GetBytes(eventData, "index").Int()
			rewritten, err := sjson.SetBytes(eventData, "index", currentIdx+1)
			if err != nil {
				// Fall through: emit original event if rewrite fails.
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
			// message_stop, ping, error, etc. — pass through untouched.
			if _, err := w.Inner.Write(event[:n]); err != nil {
				return 0, err
			}
			w.buf.Next(n)
		}
	}
	return len(data), nil
}

// captureUpstreamUsage records the input-side token counts from the upstream's
// message_start. Only input-side fields are kept: output_tokens on
// message_start is a placeholder that message_delta supersedes.
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

// mergeUpstreamUsage folds the stashed message_start counts into a
// message_delta's usage object, so the assembled message a client ends up with
// reports real input and cache tokens instead of the zeros our early
// message_start had to guess. Fields the upstream already set on the delta win.
// Returns event unchanged when there is nothing to merge or the rewrite fails —
// a mangled usage object would be worse than an incomplete one.
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

// ArmOutputProgress fires mark on output-bearing content_block_delta frames only
// (not pings or structural frames) so the native output-stall watchdog tracks
// time-since-last-output. Returns false when not streaming or without a marker.
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
