package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestStreamCostWriterAnnotatesFinalMessageDelta(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := newStreamCostWriter(rec)
	writer.SetCostCalculator(func(input, output, creation, read int) routerResponseCost {
		return routerResponseCost{
			TotalUSD:            1.25,
			InputUSD:            0.75,
			OutputUSD:           0.5,
			CacheCreationTokens: creation,
			CacheReadTokens:     read,
		}
	}, true)

	_, err := writer.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":100,\"output_tokens\":7,\"cache_read_input_tokens\":3}}\n\n"))
	require.NoError(t, err)

	var annotated gjson.Result
	for _, event := range strings.Split(rec.Body.String(), "\n\n") {
		if strings.HasPrefix(event, "event: message_delta") {
			annotated = gjson.Get(strings.TrimPrefix(strings.SplitN(event, "data: ", 2)[1], "data: "), "usage.weave_cost")
		}
	}
	require.True(t, annotated.Exists())
	require.Equal(t, float64(1.25), annotated.Get("usd").Float())
	require.Equal(t, int64(3), annotated.Get("cache_read_tokens").Int())
}

func TestStreamCostWriterPassesThroughJSONErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := newStreamCostWriter(rec)
	err := &providers.UpstreamErrorResponse{
		Status: http.StatusServiceUnavailable,
		Body:   []byte(`{"type":"error","error":{"type":"api_error","message":"upstream unavailable"}}`),
	}

	flushUpstreamErrorAsAnthropic(writer, err)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "upstream unavailable")
}

func TestResponseCostBufferFlushesAfterHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	buffer := newResponseCostBuffer(rec)
	_, err := buffer.Write([]byte(`{"ok":true}`))
	require.NoError(t, err)
	setRouterCostHeaders(buffer.Header(), routerResponseCost{
		TotalUSD:            0.5,
		InputUSD:            0.25,
		OutputUSD:           0.25,
		CacheReadTokens:     7,
		CacheCreationTokens: 11,
	})
	require.NoError(t, buffer.FlushToClient())
	require.Equal(t, "0.5", rec.Header().Get(HeaderRouterCostUSD))
	require.Equal(t, "0.25", rec.Header().Get(HeaderRouterCostInputUSD))
	require.Equal(t, "0.25", rec.Header().Get(HeaderRouterCostOutputUSD))
	require.Equal(t, "7", rec.Header().Get(HeaderRouterCacheReadTokens))
	require.Equal(t, "11", rec.Header().Get(HeaderRouterCacheCreationTokens))
	require.Equal(t, `{"ok":true}`, rec.Body.String())
}

func TestResponseCostBufferDoesNotCommitEmptyResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	buffer := newResponseCostBuffer(rec)
	require.NoError(t, buffer.FlushToClient())
	require.Empty(t, rec.Body.String())
	require.False(t, rec.Flushed)
}

func TestRouterResponseCostFromPricingRoundsFloatNoise(t *testing.T) {
	// gpt-5.4-mini pricing: 12 input + 9 output tokens yielded
	// 0.000049500000000000004 before rounding.
	pricing := catalog.Pricing{InputUSDPer1M: 0.75, OutputUSDPer1M: 4.5}
	cost := routerResponseCostFromPricing(pricing, providers.ProviderOpenAI, 12, 9, 0, 0)

	rec := httptest.NewRecorder()
	setRouterCostHeaders(rec.Header(), cost)
	require.Equal(t, "0.0000495", rec.Header().Get(HeaderRouterCostUSD))
	require.Equal(t, "0.000009", rec.Header().Get(HeaderRouterCostInputUSD))
	require.Equal(t, "0.0000405", rec.Header().Get(HeaderRouterCostOutputUSD))
}

// An Anthropic stream whose terminal message_delta carries only output tokens,
// so the annotation has to fall back to the input and cache counts remembered
// from message_start.
const streamCostFixture = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":3}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// tokenEchoingCost encodes its inputs so the annotation proves which counts
// reached the calculator.
func tokenEchoingCost(input, output, creation, read int) routerResponseCost {
	return routerResponseCost{
		TotalUSD:            float64(input+output) / 1000,
		InputUSD:            float64(input) / 1000,
		OutputUSD:           float64(output) / 1000,
		CacheCreationTokens: creation,
		CacheReadTokens:     read,
	}
}

func newAnnotatingStreamCostWriter(inner http.ResponseWriter) *streamCostWriter {
	writer := newStreamCostWriter(inner)
	writer.SetCostCalculator(tokenEchoingCost, true)
	return writer
}

// Whatever the write boundaries, the client must receive the same bytes a
// single write produces: every frame once, in order, and the terminal
// message_delta annotated with the same cost, after each boundary and at the end.
func TestStreamCostWriter_FragmentationMatchesSingleWrite(t *testing.T) {
	fixtures := []struct {
		name string
		body string
	}{
		{name: "lf framing", body: streamCostFixture},
		{name: "crlf framing", body: strings.ReplaceAll(streamCostFixture, "\n", "\r\n")},
	}
	prefixOutput := func(t *testing.T, prefix string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		writeChunks(t, newAnnotatingStreamCostWriter(rec), []string{prefix})
		return rec.Body.String()
	}
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			oracle := httptest.NewRecorder()
			writeChunks(t, newAnnotatingStreamCostWriter(oracle), []string{fx.body})
			want := oracle.Body.String()
			require.Contains(t, want, `"weave_cost":{"usd":0.02,"input_usd":0.013,"output_usd":0.007,"cache_read_tokens":3,"cache_creation_tokens":0}`,
				"the single write must annotate from message_start fallbacks so equivalence is not vacuous")

			for _, size := range chunkSizesFor(fx.body) {
				rec := httptest.NewRecorder()
				writeChunks(t, newAnnotatingStreamCostWriter(rec), chunkEvery(fx.body, size))
				assert.Equal(t, want, rec.Body.String(), "chunk size %d", size)
			}
			for i := 1; i < len(fx.body); i++ {
				rec := httptest.NewRecorder()
				writer := newAnnotatingStreamCostWriter(rec)
				writeChunks(t, writer, []string{fx.body[:i]})
				require.Equal(t, prefixOutput(t, fx.body[:i]), rec.Body.String(), "output after the first write, split at %d", i)
				writeChunks(t, writer, []string{fx.body[i:]})
				require.Equal(t, want, rec.Body.String(), "split at %d", i)
			}
		})
	}
}

// A forward write that fails leaves the event unconsumed, so the next write
// must present that same event again: nothing is lost and nothing is
// duplicated, and the annotation still sees message_start's counts.
func TestStreamCostWriter_WriteErrorBeforeConsumeRetriesSameEvent(t *testing.T) {
	oracle := httptest.NewRecorder()
	writeChunks(t, newAnnotatingStreamCostWriter(oracle), []string{streamCostFixture})
	want := oracle.Body.String()
	frames := strings.SplitAfter(streamCostFixture, "\n\n")
	require.Len(t, frames, 6, "five frames plus the empty remainder")

	t.Run("first frame", func(t *testing.T) {
		sink := &failingRecorder{ResponseRecorder: httptest.NewRecorder(), failures: 1}
		writer := newAnnotatingStreamCostWriter(sink)
		_, err := writer.Write([]byte(frames[0]))
		require.ErrorIs(t, err, errSinkClosed)
		require.Empty(t, sink.Body.String(), "the failed write delivered nothing")

		writeChunks(t, writer, frames[1:])
		assert.Equal(t, want, sink.Body.String())
	})

	t.Run("annotated terminal frame", func(t *testing.T) {
		sink := &failingRecorder{ResponseRecorder: httptest.NewRecorder()}
		writer := newAnnotatingStreamCostWriter(sink)
		writeChunks(t, writer, frames[:3])
		delivered := sink.Body.String()

		sink.failures = 1
		_, err := writer.Write([]byte(frames[3]))
		require.ErrorIs(t, err, errSinkClosed)
		require.Equal(t, delivered, sink.Body.String(), "the failed write delivered nothing")

		writeChunks(t, writer, frames[4:])
		assert.Equal(t, want, sink.Body.String())
	})

	t.Run("failure mid frame keeps the partial tail", func(t *testing.T) {
		sink := &failingRecorder{ResponseRecorder: httptest.NewRecorder(), failures: 1}
		writer := newAnnotatingStreamCostWriter(sink)
		firstAndAHalf := frames[0] + frames[1][:len(frames[1])/2]
		_, err := writer.Write([]byte(firstAndAHalf))
		require.ErrorIs(t, err, errSinkClosed)

		writeChunks(t, writer, []string{frames[1][len(frames[1])/2:]})
		writeChunks(t, writer, frames[2:])
		assert.Equal(t, want, sink.Body.String())
	})
}
