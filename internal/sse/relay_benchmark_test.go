package sse_test

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"

	"weave-os/router/internal/sse"
)

// discardFlusher is a streaming http.ResponseWriter; when dst is set it also
// retains the body so a validation pass can compare it, otherwise it drops it.
type discardFlusher struct {
	header  http.Header
	dst     *bytes.Buffer
	written int
	flushes int
}

func (d *discardFlusher) Header() http.Header { return d.header }
func (d *discardFlusher) WriteHeader(int)     {}
func (d *discardFlusher) Write(p []byte) (int, error) {
	d.written += len(p)
	if d.dst != nil {
		d.dst.Write(p)
	}
	return len(p), nil
}
func (d *discardFlusher) Flush() { d.flushes++ }

func relayStream(n int) []byte {
	var b bytes.Buffer
	b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_bench\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":100,\"output_tokens\":1}}}\n\n")
	for i := 0; i < n; i++ {
		b.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"token ")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("\"}}\n\n")
	}
	b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	return b.Bytes()
}

// relay frames stream as it arrives in chunk-byte reads and re-emits each
// event through a ChunkedWriter with one flush per event, as a passthrough
// writer does. It returns the number of events relayed.
func relay(w http.ResponseWriter, stream []byte, chunk int, pending []byte) (int, []byte) {
	chunked := sse.NewChunkedWriter(w, 4096)
	chunked.Header().Set("Content-Type", "text/event-stream")
	chunked.WriteHeader(http.StatusOK)
	var scanner sse.Scanner
	pending = pending[:0]
	events := 0
	for off := 0; off < len(stream); off += chunk {
		pending = append(pending, stream[off:min(off+chunk, len(stream))]...)
		for {
			event, consumed := scanner.Next(pending)
			if consumed == 0 {
				break
			}
			if eventType, data := sse.ParseEvent(event); len(eventType) == 0 || len(data) == 0 {
				return -1, pending
			}
			_, _ = chunked.BW.Write(event)
			_, _ = chunked.BW.WriteString("\n\n")
			_ = chunked.FlushEvent()
			events++
			pending = pending[:copy(pending, pending[consumed:])]
		}
	}
	return events, pending
}

// BenchmarkHotPathSSERelay measures framing and re-emitting an upstream SSE
// stream delivered in token-sized and buffer-sized reads.
func BenchmarkHotPathSSERelay(b *testing.B) {
	const deltas = 500
	stream := relayStream(deltas)
	for _, chunk := range []int{64, 4096} {
		b.Run("chunk="+strconv.Itoa(chunk), func(b *testing.B) {
			var emitted bytes.Buffer
			capture := &discardFlusher{header: http.Header{}, dst: &emitted}
			pending := make([]byte, 0, 2*chunk)
			events, _ := relay(capture, stream, chunk, pending)
			if events != deltas+2 || capture.written != len(stream) || capture.flushes != events || emitted.String() != string(stream) {
				b.Fatalf("relayed %d events, %d bytes, %d flushes; want %d events, %d bytes verbatim", events, capture.written, capture.flushes, deltas+2, len(stream))
			}
			out := &discardFlusher{header: http.Header{}}
			b.SetBytes(int64(len(stream)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, pending = relay(out, stream, chunk, pending)
			}
		})
	}
}
