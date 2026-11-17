package recordedlaws_test

import (
	"bytes"
	"strings"
)

type sseFrame struct {
	event string
	data  string
}

func parseSSE(body []byte) []sseFrame {
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	var frames []sseFrame
	for _, block := range strings.Split(normalized, "\n\n") {
		var frame sseFrame
		var data []string
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "event:"); ok {
				frame.event = strings.TrimPrefix(v, " ")
			}
			if v, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimPrefix(v, " "))
			}
		}
		if frame.event == "" && len(data) == 0 {
			continue
		}
		frame.data = strings.TrimSpace(strings.Join(data, "\n"))
		frames = append(frames, frame)
	}
	return frames
}

func looksLikeSSE(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("data:"))
}
