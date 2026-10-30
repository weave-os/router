package sse_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/sse"
)

func TestWriteJSONChunkPreservesFragmentedSSE(t *testing.T) {
	const payload = "data: {\"text\":\"</script><script>&☃\",\"large\":9007199254740993}\n\n"
	for split := 0; split <= len(payload); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			sink := httptest.NewRecorder()
			sink.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			for _, chunk := range []string{payload[:split], payload[split:]} {
				consumed, err := sse.WriteJSONChunk(sink, []byte(chunk))
				require.NoError(t, err)
				require.Equal(t, len(chunk), consumed)
			}
			require.NotContains(t, sink.Body.String(), "<script>")
			_, rewritten := sse.ParseEvent(sink.Body.Bytes())
			_, original := sse.ParseEvent([]byte(payload))
			require.JSONEq(t, string(original), string(rewritten))
			require.Equal(t, "text/event-stream", sink.Result().Header.Get("Content-Type"))
		})
	}
}

func TestWriteJSONChunkMatchesStandardHTMLEscape(t *testing.T) {
	inputs := [][]byte{
		[]byte(`{"text":"<script>& \" ' &lt;&amp;&#34;&#39;","large":9007199254740993}`),
		[]byte("data: {\"text\":\"☃\u2028\u2029\"}\n\n"),
		{0xff, '<', 0xe2, 0x80},
	}
	for _, input := range inputs {
		for split := 0; split <= len(input); split++ {
			for _, chunk := range [][]byte{input[:split], input[split:]} {
				var expected bytes.Buffer
				json.HTMLEscape(&expected, chunk)
				sink := httptest.NewRecorder()
				consumed, err := sse.WriteJSONChunk(sink, chunk)
				require.NoError(t, err)
				require.Equal(t, len(chunk), consumed)
				require.Equal(t, expected.Bytes(), sink.Body.Bytes())
			}
		}
	}
}

func TestPrepareJSONResponseRemovesCommittedLength(t *testing.T) {
	const payload = `{"text":"<script>"}`
	writeErrors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		sse.PrepareJSONResponse(w)
		w.WriteHeader(http.StatusOK)
		_, err := sse.WriteJSONChunk(w, []byte(payload))
		writeErrors <- err
	}))
	defer server.Close()
	response, err := server.Client().Get(server.URL)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, <-writeErrors)
	require.Equal(t, "application/json", response.Header.Get("Content-Type"))
	require.Equal(t, "nosniff", response.Header.Get("X-Content-Type-Options"))
	require.JSONEq(t, payload, string(body))
	require.NotContains(t, string(body), "<script>")
}

type shortResponseWriter struct{ *httptest.ResponseRecorder }

func (w shortResponseWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestWriteJSONChunkReportsShortWrites(t *testing.T) {
	consumed, err := sse.WriteJSONChunk(shortResponseWriter{httptest.NewRecorder()}, []byte(`{"text":"<script>"}`))
	require.ErrorIs(t, err, io.ErrShortWrite)
	require.Zero(t, consumed)
}

func TestWriteJSONChunkDoesNotDoubleEscape(t *testing.T) {
	const payload = `{"text":"<script>&","escaped":"\u003c"}`
	sink := httptest.NewRecorder()
	_, err := sse.WriteJSONChunk(sink, []byte(payload))
	require.NoError(t, err)
	escapedOnce := bytes.Clone(sink.Body.Bytes())
	sink.Body.Reset()
	_, err = sse.WriteJSONChunk(sink, escapedOnce)
	require.NoError(t, err)
	require.Equal(t, escapedOnce, sink.Body.Bytes())
	require.True(t, json.Valid(sink.Body.Bytes()))
	require.JSONEq(t, payload, sink.Body.String())
}
