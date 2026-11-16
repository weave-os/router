package proxy

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/router"
)

const upstreamOpenAIErrorBody = `{"error":{"type":"invalid_request_error","message":"bad tool schema"}}`

func encodedErrorUpstream(t *testing.T, encoding string) *httptest.Server {
	t.Helper()
	var encoded bytes.Buffer
	var writer io.WriteCloser
	switch encoding {
	case "gzip":
		writer = gzip.NewWriter(&encoded)
	case "deflate":
		writer = zlib.NewWriter(&encoded)
	default:
		t.Fatalf("unsupported test encoding %q", encoding)
	}
	_, err := writer.Write([]byte(upstreamOpenAIErrorBody))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", encoding)
		w.Header().Set("X-Request-Id", "req_synthetic")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(encoded.Bytes())
	}))
	t.Cleanup(server.Close)
	return server
}

func upstreamErrorThroughAdapter(t *testing.T, encoding string) error {
	t.Helper()
	client := openaicompat.NewClient("test-key", encodedErrorUpstream(t, encoding).URL)
	inbound := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/chat/completions", nil)
	err := client.Proxy(context.Background(), router.Decision{Provider: providers.ProviderOpenRouter, Model: "test-model"},
		providers.PreparedRequest{Body: []byte(`{"model":"test-model","messages":[]}`)}, httptest.NewRecorder(), inbound)
	var upstreamErr *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &upstreamErr)
	return err
}

func bodyAsClientDecodesIt(t *testing.T, rec *httptest.ResponseRecorder) []byte {
	t.Helper()
	var reader io.Reader = bytes.NewReader(rec.Body.Bytes())
	switch encoding := rec.Header().Get("Content-Encoding"); encoding {
	case "":
	case "gzip":
		gz, err := gzip.NewReader(reader)
		require.NoError(t, err, "body is not gzip though Content-Encoding claims it")
		reader = gz
	case "deflate":
		zr, err := zlib.NewReader(reader)
		require.NoError(t, err, "body is not deflate though Content-Encoding claims it")
		reader = zr
	default:
		t.Fatalf("unexpected Content-Encoding %q", encoding)
	}
	decoded, err := io.ReadAll(reader)
	require.NoError(t, err)
	return decoded
}

var replayedErrorFlushes = map[string]func(http.ResponseWriter, error){
	"buffered":  flushBufferedIfPresent,
	"openai":    flushUpstreamErrorAsOpenAI,
	"anthropic": flushUpstreamErrorAsAnthropic,
}

func TestReplayedUpstreamErrorBodyMatchesContentEncoding(t *testing.T) {
	for _, encoding := range []string{"gzip", "deflate"} {
		for name, flush := range replayedErrorFlushes {
			t.Run(encoding+"/"+name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				flush(rec, upstreamErrorThroughAdapter(t, encoding))

				assert.Equal(t, http.StatusBadRequest, rec.Code)
				assert.Equal(t, "req_synthetic", rec.Header().Get("X-Request-Id"))
				assert.Empty(t, rec.Header().Get("Content-Length"))
				decoded := bodyAsClientDecodesIt(t, rec)
				assert.True(t, json.Valid(decoded), "client decodes %q per Content-Encoding %q", decoded, rec.Header().Get("Content-Encoding"))
			})
		}
	}
}

func TestReplayedUpstreamErrorTransportDecodesGzip(t *testing.T) {
	var upstreamErr *providers.UpstreamErrorResponse
	require.ErrorAs(t, upstreamErrorThroughAdapter(t, "gzip"), &upstreamErr)
	assert.Empty(t, upstreamErr.Headers.Get("Content-Encoding"))
	assert.JSONEq(t, upstreamOpenAIErrorBody, string(upstreamErr.Body))

	require.ErrorAs(t, upstreamErrorThroughAdapter(t, "deflate"), &upstreamErr)
	assert.Equal(t, "deflate", upstreamErr.Headers.Get("Content-Encoding"))
	assert.False(t, json.Valid(upstreamErr.Body), "an encoding the transport did not request reaches the error raw")
}

func TestRewrittenUpstreamErrorDropsContentEncoding(t *testing.T) {
	rec := httptest.NewRecorder()
	flushUpstreamErrorAsAnthropic(rec, &providers.UpstreamErrorResponse{
		Status:  http.StatusBadRequest,
		Headers: http.Header{"Content-Encoding": {"gzip"}, "X-Request-Id": {"req_synthetic"}},
		Body:    []byte(upstreamOpenAIErrorBody),
	})

	assert.JSONEq(t, `{"type":"error","error":{"type":"invalid_request_error","message":"bad tool schema"}}`, rec.Body.String())
	assert.Empty(t, rec.Header().Get("Content-Encoding"), "a re-rendered body is no longer the upstream's encoded bytes")
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "req_synthetic", rec.Header().Get("X-Request-Id"))
}
