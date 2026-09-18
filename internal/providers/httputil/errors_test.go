package httputil

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCapped_TruncatesAndDrainsRest(t *testing.T) {
	body := strings.Repeat("a", 10) + strings.Repeat("b", 10)
	prefix, total, err := ReadCapped(strings.NewReader(body), 10)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 10), string(prefix))
	assert.EqualValues(t, 20, total)
}

func TestPreviewBytes_CapsAt1KB(t *testing.T) {
	body := []byte(strings.Repeat("x", 2000))
	preview := PreviewBytes(body)
	assert.Len(t, preview, 1024)

	small := []byte("short body")
	assert.Equal(t, "short body", PreviewBytes(small))
}

func TestHeaderCapture_CapturesHeaderWithoutWriting(t *testing.T) {
	h := http.Header{}
	hc := HeaderCapture{H: h}
	hc.Header().Set("X-Test", "value")
	n, err := hc.Write([]byte("ignored"))
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, "value", h.Get("X-Test"))
}

func TestWritePassthroughError_WritesBodyLogsAndReturnsStatusError(t *testing.T) {
	upstreamBody := "upstream failure detail"
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       http.NoBody,
	}
	resp.Body = io.NopCloser(strings.NewReader(upstreamBody))

	rec := httptest.NewRecorder()
	var firstByteCalls, eofCalls int
	err := WritePassthroughError(context.Background(), rec, resp, func() { firstByteCalls++ }, func() { eofCalls++ }, "upstream failed", "path", "/v1/messages")

	var statusErr *providers.UpstreamStatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, http.StatusBadGateway, statusErr.Status)
	assert.Equal(t, upstreamBody, rec.Body.String())
	assert.Equal(t, 1, firstByteCalls)
	assert.Equal(t, 1, eofCalls)
}

func TestWritePassthroughError_NilHooksAreSafe(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader("boom")),
	}
	rec := httptest.NewRecorder()
	err := WritePassthroughError(context.Background(), rec, resp, nil, nil, "upstream failed")
	require.Error(t, err)
	assert.Equal(t, "boom", rec.Body.String())
}

func TestLogUpstreamStatus_DropsBodyPreviewWhenContentLoggingDisallowed(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ctx := observability.WithLogger(
		requestcontext.WithContentLogging(context.Background(), false),
		log,
	)

	LogUpstreamStatus(ctx, "upstream failed", http.StatusBadRequest, "body_preview", "secret-echo", "model", "m")

	assert.NotContains(t, buf.String(), "secret-echo")
	assert.NotContains(t, buf.String(), "body_preview")
	assert.Contains(t, buf.String(), "model=m")
}

func TestLogUpstreamStatus_KeepsBodyPreviewWhenContentLoggingAllowed(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ctx := observability.WithLogger(
		requestcontext.WithContentLogging(context.Background(), true),
		log,
	)

	LogUpstreamStatus(ctx, "upstream failed", http.StatusBadRequest, "body_preview", "err-echo")

	assert.Contains(t, buf.String(), "err-echo")
}
