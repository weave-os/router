package sse_test

import (
	"strconv"
	"strings"
	"testing"

	"weave-os/router/internal/sse"
)

var splitNextBufferedBenchmarkSink []byte

type splitNextNewlineStyle string

const (
	splitNextNewlineLF    splitNextNewlineStyle = "lf"
	splitNextNewlineCRLF  splitNextNewlineStyle = "crlf"
	splitNextNewlineMixed splitNextNewlineStyle = "mixed"
)

func BenchmarkSplitNextBuffered(b *testing.B) {
	for _, frameCount := range []int{1024, 2048, 4096, 8192} {
		for _, newlineStyle := range []splitNextNewlineStyle{splitNextNewlineLF, splitNextNewlineCRLF, splitNextNewlineMixed} {
			b.Run(string(newlineStyle)+"/"+strconv.Itoa(frameCount), func(b *testing.B) {
				body := splitNextBenchmarkBody(frameCount, newlineStyle)
				if splitNextBenchmarkFrameCount(body) != frameCount {
					b.Fatalf("fixture produced the wrong frame count")
				}
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					remaining := body
					for len(remaining) > 0 {
						event, consumed := sse.SplitNext(remaining)
						if consumed == 0 {
							b.Fatal("complete fixture did not split")
						}
						splitNextBufferedBenchmarkSink = event
						remaining = remaining[consumed:]
					}
				}
			})
		}
	}
}

func splitNextBenchmarkBody(frameCount int, newlineStyle splitNextNewlineStyle) []byte {
	var body strings.Builder
	for i := 0; i < frameCount; i++ {
		body.WriteString("data: frame-")
		body.WriteString(strconv.Itoa(i))
		if newlineStyle == splitNextNewlineCRLF || (newlineStyle == splitNextNewlineMixed && i%2 == 1) {
			body.WriteString("\r\n\r\n")
		} else {
			body.WriteString("\n\n")
		}
	}
	return []byte(body.String())
}

func splitNextBenchmarkFrameCount(body []byte) int {
	count := 0
	for len(body) > 0 {
		_, consumed := sse.SplitNext(body)
		if consumed == 0 {
			return count
		}
		count++
		body = body[consumed:]
	}
	return count
}
