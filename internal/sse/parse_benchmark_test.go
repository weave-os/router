package sse_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"weave-os/router/internal/sse"
)

var splitNextBufferedBenchmarkSink []byte

// splitNextFrameShape names where the delimiter sits in a large single frame.
type splitNextFrameShape string

const (
	splitNextFrameLFEnd       splitNextFrameShape = "lf-end"
	splitNextFrameCRLFEnd     splitNextFrameShape = "crlf-end"
	splitNextFrameAbsent      splitNextFrameShape = "absent"
	splitNextFrameNewlineRich splitNextFrameShape = "newline-rich"
)

const benchmarkProviderChunk = 4096

// BenchmarkSplitNextLargeFrame measures one stateless call over a single
// large frame: the delimiter at the very end (LF or CRLF), no delimiter at
// all, or a payload with a line break every 64 bytes so the LF candidate
// search has to visit many non-delimiter newlines.
func BenchmarkSplitNextLargeFrame(b *testing.B) {
	for _, frameSize := range []int{4 << 10, 64 << 10, 1 << 20} {
		for _, shape := range []splitNextFrameShape{splitNextFrameLFEnd, splitNextFrameCRLFEnd, splitNextFrameAbsent, splitNextFrameNewlineRich} {
			b.Run(string(shape)+"/"+strconv.Itoa(frameSize), func(b *testing.B) {
				body, wantConsumed := splitNextLargeFrame(frameSize, shape)
				_, consumed := sse.SplitNext(body)
				if consumed != wantConsumed {
					b.Fatalf("fixture split consumed %d bytes, want %d", consumed, wantConsumed)
				}
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					event, _ := sse.SplitNext(body)
					splitNextBufferedBenchmarkSink = event
				}
			})
		}
	}
}

// BenchmarkSplitNextDelimiterNearStart checks the search stops at the first
// delimiter instead of scanning the whole buffer behind it.
func BenchmarkSplitNextDelimiterNearStart(b *testing.B) {
	for _, bufferSize := range []int{64 << 10, 1 << 20} {
		b.Run(strconv.Itoa(bufferSize), func(b *testing.B) {
			body := append([]byte("data: first\n\n"), bytes.Repeat([]byte{'x'}, bufferSize)...)
			_, consumed := sse.SplitNext(body)
			if consumed != len("data: first\n\n") {
				b.Fatal("fixture did not split at the leading frame")
			}
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				event, _ := sse.SplitNext(body)
				splitNextBufferedBenchmarkSink = event
			}
		})
	}
}

// BenchmarkScannerChunkedFrame delivers one large frame in provider-sized
// chunks into an append buffer and drains after every write, the way the
// buffered translators do. "scanner" resumes with sse.Scanner; "stateless"
// re-runs SplitNext from the buffer start on every write, which is the
// quadratic behavior the scanner exists to remove.
func BenchmarkScannerChunkedFrame(b *testing.B) {
	for _, frameSize := range []int{16 << 10, 64 << 10, 256 << 10, 1 << 20} {
		body, wantConsumed := splitNextLargeFrame(frameSize, splitNextFrameLFEnd)
		chunks := splitNextChunks(body, benchmarkProviderChunk)
		b.Run("scanner/"+strconv.Itoa(frameSize), func(b *testing.B) {
			requireScannerDrain(b, chunks, 1, wantConsumed)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				drainChunksWithScanner(chunks)
			}
		})
		b.Run("stateless/"+strconv.Itoa(frameSize), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				drainChunksStateless(chunks)
			}
		})
	}
}

// BenchmarkScannerShortChunks streams many small frames plus an unterminated
// tail in chunks smaller than a frame, so most writes complete zero or one
// event and the per-write cursor bookkeeping dominates.
func BenchmarkScannerShortChunks(b *testing.B) {
	for _, chunkSize := range []int{16, 64, 512} {
		body := append(splitNextBenchmarkBody(2048, splitNextNewlineMixed), "data: unterminated tail"...)
		chunks := splitNextChunks(body, chunkSize)
		b.Run("scanner/"+strconv.Itoa(chunkSize), func(b *testing.B) {
			requireScannerDrain(b, chunks, 2048, len(body)-len("data: unterminated tail"))
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				drainChunksWithScanner(chunks)
			}
		})
		b.Run("stateless/"+strconv.Itoa(chunkSize), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				drainChunksStateless(chunks)
			}
		})
	}
}

// BenchmarkScannerResetPerAttempt models an owner that resets its
// bytes.Buffer and scanner between attempts, then frames a short stream.
func BenchmarkScannerResetPerAttempt(b *testing.B) {
	body := splitNextBenchmarkBody(16, splitNextNewlineLF)
	chunks := splitNextChunks(body, 7)
	var scanner sse.Scanner
	var buf bytes.Buffer
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		scanner.Reset()
		for _, chunk := range chunks {
			buf.Write(chunk)
			for {
				event, n := scanner.Next(buf.Bytes())
				if n == 0 {
					break
				}
				splitNextBufferedBenchmarkSink = event
				buf.Next(n)
			}
		}
	}
}

func splitNextLargeFrame(frameSize int, shape splitNextFrameShape) (body []byte, wantConsumed int) {
	body = make([]byte, 0, frameSize+4)
	body = append(body, "data: "...)
	switch shape {
	case splitNextFrameNewlineRich:
		for len(body) < frameSize {
			body = append(body, bytes.Repeat([]byte{'x'}, 63)...)
			body = append(body, '\n')
		}
		body = body[:frameSize]
		if body[len(body)-1] == '\n' {
			body[len(body)-1] = 'x'
		}
	default:
		body = append(body, bytes.Repeat([]byte{'x'}, frameSize-len(body))...)
	}
	switch shape {
	case splitNextFrameCRLFEnd:
		body = append(body, "\r\n\r\n"...)
	case splitNextFrameAbsent:
		return body, 0
	default:
		body = append(body, "\n\n"...)
	}
	return body, len(body)
}

func splitNextChunks(body []byte, chunkSize int) [][]byte {
	var chunks [][]byte
	for start := 0; start < len(body); start += chunkSize {
		chunks = append(chunks, body[start:min(start+chunkSize, len(body))])
	}
	return chunks
}

func requireScannerDrain(b *testing.B, chunks [][]byte, wantEvents, wantConsumed int) {
	b.Helper()
	events, consumed := drainChunksWithScanner(chunks)
	if events != wantEvents || consumed != wantConsumed {
		b.Fatalf("fixture drained %d events over %d bytes, want %d events over %d bytes", events, consumed, wantEvents, wantConsumed)
	}
	statelessEvents, statelessConsumed := drainChunksStateless(chunks)
	if statelessEvents != wantEvents || statelessConsumed != wantConsumed {
		b.Fatalf("stateless baseline drained %d events over %d bytes, want %d events over %d bytes", statelessEvents, statelessConsumed, wantEvents, wantConsumed)
	}
}

func drainChunksWithScanner(chunks [][]byte) (events, consumed int) {
	var scanner sse.Scanner
	var buf []byte
	for _, chunk := range chunks {
		buf = append(buf, chunk...)
		for {
			event, n := scanner.Next(buf)
			if n == 0 {
				break
			}
			splitNextBufferedBenchmarkSink = event
			events++
			consumed += n
			buf = buf[n:]
		}
	}
	return events, consumed
}

func drainChunksStateless(chunks [][]byte) (events, consumed int) {
	var buf []byte
	for _, chunk := range chunks {
		buf = append(buf, chunk...)
		for {
			event, n := sse.SplitNext(buf)
			if n == 0 {
				break
			}
			splitNextBufferedBenchmarkSink = event
			events++
			consumed += n
			buf = buf[n:]
		}
	}
	return events, consumed
}

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

func BenchmarkScannerOnly(b *testing.B) {
	for _, frameSize := range []int{4096, 16384, 65536, 262144, 1048576} {
		b.Run(strconv.Itoa(frameSize), func(b *testing.B) {
			body, wantConsumed := splitNextLargeFrame(frameSize, splitNextFrameLFEnd)
			var check sse.Scanner
			for end := benchmarkProviderChunk; end-benchmarkProviderChunk < len(body); end += benchmarkProviderChunk {
				_, consumed := check.Next(body[:min(end, len(body))])
				if end >= len(body) && consumed != wantConsumed {
					b.Fatal("fixture did not complete at its final chunk")
				}
			}
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var scanner sse.Scanner
				for end := benchmarkProviderChunk; end-benchmarkProviderChunk < len(body); end += benchmarkProviderChunk {
					event, _ := scanner.Next(body[:min(end, len(body))])
					splitNextBufferedBenchmarkSink = event
				}
			}
		})
	}
}
