package sse

import "bytes"

// SplitNext returns the next complete SSE event in buf (without delimiter) and the total bytes consumed; n=0 means no complete event yet. Accepts both LF (\n\n) and CRLF (\r\n\r\n) boundaries.
func SplitNext(buf []byte) (event []byte, n int) {
	return splitFrom(buf, 0)
}

// Search LF candidates in order; checking the preceding CR preserves the
// earliest-delimiter rule without scanning the whole suffix twice.
func splitFrom(buf []byte, start int) (event []byte, n int) {
	for i := start; i < len(buf); i++ {
		offset := bytes.IndexByte(buf[i:], '\n')
		if offset < 0 {
			return nil, 0
		}
		i += offset
		if i+1 < len(buf) && buf[i+1] == '\n' {
			return buf[:i], i + 2
		}
		if i > 0 && buf[i-1] == '\r' && i+2 < len(buf) && buf[i+1] == '\r' && buf[i+2] == '\n' {
			return buf[:i-1], i + 3
		}
	}
	return nil, 0
}

// ParseEvent extracts the event type and data payload from a single SSE
// event without allocating. Both return values are subslices of the input.
// Multi-line data: fields return only the first line's content, which is
// sufficient for the single-line JSON payloads both Anthropic and OpenAI emit.
func ParseEvent(event []byte) (eventType, data []byte) {
	remaining := event
	for len(remaining) > 0 {
		var line []byte
		if idx := bytes.IndexByte(remaining, '\n'); idx >= 0 {
			line = remaining[:idx]
			remaining = remaining[idx+1:]
		} else {
			line = remaining
			remaining = nil
		}
		line = bytes.TrimRight(line, "\r")

		if bytes.HasPrefix(line, []byte("event:")) {
			eventType = bytes.TrimSpace(line[6:])
		} else if data == nil && bytes.HasPrefix(line, []byte("data:")) {
			data = bytes.TrimSpace(line[5:])
		}
	}
	return eventType, data
}
