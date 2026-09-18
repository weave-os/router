package translate_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"weave-os/router/internal/translate"
)

// writerChainBenchmark is one representative writer driven the way the proxy
// drives it, plus a substring proving the fixture was really translated.
type writerChainBenchmark struct {
	name   string
	body   []byte
	expect string
	build  writerBuilder
}

// writerBuilder constructs a writer over rec and returns it with its Finalize
// (nil for passthrough writers that have none).
type writerBuilder func(b *testing.B, rec *httptest.ResponseRecorder) (http.ResponseWriter, func() error)

func buildSSETranslator(b *testing.B, rec *httptest.ResponseRecorder) (http.ResponseWriter, func() error) {
	b.Helper()
	w := translate.NewSSETranslator(rec, "claude-opus-4", nil)
	commitUpstreamResponse(w, true)
	return w, w.Finalize
}

func buildAnthropicSSETranslator(b *testing.B, rec *httptest.ResponseRecorder) (http.ResponseWriter, func() error) {
	b.Helper()
	w := translate.NewAnthropicSSETranslator(rec, "gpt-x", nil)
	err := w.Prelude(true)
	if err != nil {
		b.Fatal(err)
	}
	return w, w.Finalize
}

func buildResponsesToAnthropicWriter(b *testing.B, rec *httptest.ResponseRecorder) (http.ResponseWriter, func() error) {
	b.Helper()
	w := translate.NewResponsesToAnthropicWriter(rec, "gpt-5.5", nil)
	err := w.Prelude(true)
	if err != nil {
		b.Fatal(err)
	}
	return w, w.Finalize
}

func buildAnthropicRoutingFooterWriter(b *testing.B, rec *httptest.ResponseRecorder) (http.ResponseWriter, func() error) {
	b.Helper()
	w := translate.NewAnthropicRoutingFooterWriter(rec, testFooter)
	commitUpstreamResponse(w, true)
	return w, nil
}

func buildOpenAIRoutingMarkerWriter(b *testing.B, rec *httptest.ResponseRecorder) (http.ResponseWriter, func() error) {
	b.Helper()
	w := translate.NewOpenAIRoutingMarkerWriter(rec, "gpt-x", testMarker)
	commitUpstreamResponse(w, true)
	w.ArmOutputProgress(func() {})
	return w, nil
}

func writerChainBenchmarks() []writerChainBenchmark {
	return []writerChainBenchmark{
		{name: "SSETranslator/anthropic-text", body: []byte(largeAnthropicStream()), expect: "data: [DONE]", build: buildSSETranslator},
		{name: "AnthropicSSETranslator/openai-text", body: []byte(largeOpenAIChunkStream()), expect: "event: message_stop", build: buildAnthropicSSETranslator},
		{name: "ResponsesToAnthropicWriter/text-and-tool-args", body: []byte(largeResponsesStream()), expect: `"name":"write_file"`, build: buildResponsesToAnthropicWriter},
		{name: "AnthropicRoutingFooterWriter/anthropic-text", body: []byte(largeAnthropicStream()), expect: "Weave Router feedback", build: buildAnthropicRoutingFooterWriter},
		{name: "OpenAIRoutingMarkerWriter/openai-text", body: []byte(largeOpenAIChunkStream()), expect: "data: [DONE]", build: buildOpenAIRoutingMarkerWriter},
	}
}

func runWriterChain(b *testing.B, chain writerChainBenchmark, chunks [][]byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	w, finalize := chain.build(b, rec)
	for _, chunk := range chunks {
		_, err := w.Write(chunk)
		if err != nil {
			b.Fatal(err)
		}
	}
	if finalize != nil {
		err := finalize()
		if err != nil {
			b.Fatal(err)
		}
	}
	return rec
}

func requireWriterChainOutput(b *testing.B, chain writerChainBenchmark, chunks [][]byte) {
	b.Helper()
	rec := runWriterChain(b, chain, chunks)
	if !strings.Contains(rec.Body.String(), chain.expect) {
		b.Fatalf("fixture output for %s lacks %q", chain.name, chain.expect)
	}
}

func benchmarkWriterChain(b *testing.B, chain writerChainBenchmark, chunks [][]byte) {
	requireWriterChainOutput(b, chain, chunks)
	b.SetBytes(int64(len(chain.body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runWriterChain(b, chain, chunks)
	}
}

// BenchmarkStreamWriterChunkedWrites feeds each representative writer a
// large fixture in fixed-size writes, from well under a frame to a
// provider-sized 4 KiB, so the cost of re-framing across writes is measured
// through the real Write path rather than the scanner alone.
func BenchmarkStreamWriterChunkedWrites(b *testing.B) {
	for _, chain := range writerChainBenchmarks() {
		for _, chunkSize := range []int{64, 512, 4096} {
			b.Run(chain.name+"/"+strconv.Itoa(chunkSize), func(b *testing.B) {
				benchmarkWriterChain(b, chain, chunkEvery(chain.body, chunkSize))
			})
		}
	}
}

// BenchmarkStreamWriterGrowingFrame delivers one frame of growing size in
// 4 KiB writes through a passthrough writer and a translating writer; the
// slope across sizes shows whether framing cost tracks the frame (linear) or
// every write rescans it (quadratic).
func BenchmarkStreamWriterGrowingFrame(b *testing.B) {
	for _, frameBytes := range []int{16 << 10, 64 << 10, 256 << 10} {
		chains := []writerChainBenchmark{
			{name: "AnthropicRoutingFooterWriter", body: []byte(largeAnthropicStreamOf(frameBytes, 0)), expect: "Weave Router feedback", build: buildAnthropicRoutingFooterWriter},
			{name: "AnthropicSSETranslator", body: []byte(largeOpenAIChunkStreamOf(frameBytes, 0)), expect: "event: message_stop", build: buildAnthropicSSETranslator},
		}
		for _, chain := range chains {
			b.Run(chain.name+"/"+strconv.Itoa(frameBytes), func(b *testing.B) {
				benchmarkWriterChain(b, chain, chunkEvery(chain.body, 4096))
			})
		}
	}
}

func BenchmarkStreamWriterFullChain(b *testing.B) {
	for _, frameBytes := range []int{16 << 10, 64 << 10, 256 << 10} {
		body := []byte(largeOpenAIChunkStreamOf(frameBytes, 0))
		chunks := chunkEvery(body, 4096)
		b.Run(strconv.Itoa(frameBytes), func(b *testing.B) {
			chain := writerChainBenchmark{
				name:   "openai-to-anthropic-marker-footer",
				body:   body,
				expect: "Weave Router feedback",
				build: func(b *testing.B, rec *httptest.ResponseRecorder) (http.ResponseWriter, func() error) {
					footer := translate.NewAnthropicRoutingFooterWriter(rec, testFooter)
					marker := translate.NewAnthropicRoutingMarkerWriter(footer, "gpt-x", testMarker)
					writer := translate.NewAnthropicSSETranslator(marker, "gpt-x", nil)
					if err := writer.Prelude(true); err != nil {
						b.Fatal(err)
					}
					return writer, writer.Finalize
				},
			}
			benchmarkWriterChain(b, chain, chunks)
		})
	}
}
