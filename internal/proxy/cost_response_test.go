package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workweave/router/internal/providers"

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
