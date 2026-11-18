package sse

import "bytes"

func SplitNext(buf []byte) (event []byte, n int) {
	return splitFrom(buf, 0)
}

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
